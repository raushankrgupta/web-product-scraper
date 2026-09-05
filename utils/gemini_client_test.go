package utils

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/generative-ai-go/genai"
)

// The regression this guards: a safety-blocked candidate arrives with
// Content == nil, and the old
//
//	len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0
//
// short-circuited on the left, dereferenced nil on the right, and panicked
// the whole process — with no recovery middleware to catch it.
func TestExtractImageBlockedCandidateDoesNotPanic(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content:      nil,
				FinishReason: genai.FinishReasonSafety,
				SafetyRatings: []*genai.SafetyRating{
					{Category: genai.HarmCategorySexuallyExplicit, Probability: genai.HarmProbabilityLow, Blocked: false},
				},
			},
		},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("extractImage panicked on a blocked candidate: %v", r)
		}
	}()

	got, err := extractImage(resp, "test", "test-model", time.Second)
	if err == nil {
		t.Fatal("expected an error for a blocked candidate")
	}
	if got != nil {
		t.Fatalf("expected no bytes, got %d", len(got))
	}
	// The finish reason must survive into the error — that is what turns
	// "no content generated" into a one-line diagnosis.
	if !strings.Contains(err.Error(), "SAFETY(3)") {
		t.Errorf("error %q does not carry the finish reason", err)
	}
}

func TestExtractImageNoCandidatesReportsBlockReason(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: nil,
		PromptFeedback: &genai.PromptFeedback{
			BlockReason: genai.BlockReasonOther,
			SafetyRatings: []*genai.SafetyRating{
				{Category: genai.HarmCategoryDangerousContent, Probability: genai.HarmProbabilityNegligible},
			},
		},
	}

	_, err := extractImage(resp, "test", "test-model", time.Second)
	if err == nil {
		t.Fatal("expected an error when no candidates are returned")
	}
	if !strings.Contains(err.Error(), "prompt blocked") {
		t.Errorf("error %q should say the prompt was blocked", err)
	}
	if !strings.Contains(err.Error(), "Other") {
		t.Errorf("error %q should carry the block reason", err)
	}
}

// A TEXT response on an image-generation call must be an error, not "image
// bytes". Accepting it is what uploaded an English sentence to S3 as
// image/jpeg and handed the user a presigned URL to it.
func TestExtractImageRejectsTextResponse(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{Content: &genai.Content{Parts: []genai.Part{
				genai.Text("I'm sorry, I can't help with that request."),
			}}},
		},
	}

	got, err := extractImage(resp, "test", "test-model", time.Second)
	if err == nil {
		t.Fatal("a TEXT response must be rejected on an image-generation call")
	}
	if got != nil {
		t.Fatalf("expected no bytes, got %q", got)
	}
	if !strings.Contains(err.Error(), "text instead of an image") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExtractImageReturnsBlob(t *testing.T) {
	want := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{Content: &genai.Content{Parts: []genai.Part{
				genai.Blob{MIMEType: "image/jpeg", Data: want},
			}}},
		},
	}

	got, err := extractImage(resp, "test", "test-model", time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExtractImageEmptyPartsIsAnError(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: nil}}},
	}
	if _, err := extractImage(resp, "test", "test-model", time.Second); err == nil {
		t.Fatal("expected an error for a candidate with no parts")
	}
}

func TestExtractImageNilResponse(t *testing.T) {
	if _, err := extractImage(nil, "test", "test-model", time.Second); err == nil {
		t.Fatal("expected an error for a nil response")
	}
}

func TestFormatRatings(t *testing.T) {
	got := formatRatings([]*genai.SafetyRating{
		{Category: genai.HarmCategorySexuallyExplicit, Probability: genai.HarmProbabilityLow, Blocked: false},
		nil, // must be skipped, not dereferenced
		{Category: genai.HarmCategoryDangerousContent, Probability: genai.HarmProbabilityHigh, Blocked: true},
	})
	if !strings.Contains(got, "blocked=true") || !strings.Contains(got, "blocked=false") {
		t.Errorf("formatRatings() = %q, want both blocked states rendered", got)
	}
	if formatRatings(nil) != "" {
		t.Error("formatRatings(nil) should be empty")
	}
}

// FinishReason 11 and 15 are what a refused try-on actually returns. The SDK's
// enum stops at 5, so both used to log as "FinishReason(11)" / "FinishReason(15)"
// — a number nobody could look up, on the one code path where the number is the
// whole diagnosis.
func TestFinishReasonNameDecodesImageReasons(t *testing.T) {
	cases := map[genai.FinishReason]string{
		1:  "STOP(1)",
		3:  "SAFETY(3)",
		11: "IMAGE_SAFETY(11)",
		14: "IMAGE_PROHIBITED_CONTENT(14)",
		15: "IMAGE_OTHER(15)",
		16: "NO_IMAGE(16)",
		99: "UNKNOWN(99)",
	}
	for fr, want := range cases {
		if got := finishReasonName(fr); got != want {
			t.Errorf("finishReasonName(%d) = %q, want %q", int32(fr), got, want)
		}
	}
}

func TestBlockErrorIsASafetyBlock(t *testing.T) {
	err := &blockError{Reason: 11}
	if !isSafetyBlock(err) {
		t.Error("a blockError must count as a safety block so the retry path runs")
	}
	if !strings.Contains(err.Error(), "IMAGE_SAFETY(11)") {
		t.Errorf("error %q should name the reason", err)
	}
}

// PROHIBITED_CONTENT and friends are decided on the bytes we sent. Retrying
// with a shorter prompt spends another billed generation on the same answer.
func TestIsTerminalBlock(t *testing.T) {
	terminal := []genai.FinishReason{4, 7, 8, 9, 14, 17}
	for _, fr := range terminal {
		if !isTerminalBlock(&blockError{Reason: fr}) {
			t.Errorf("finish reason %s should be terminal", finishReasonName(fr))
		}
	}
	retryable := []genai.FinishReason{3, 5, 11, 15, 16}
	for _, fr := range retryable {
		if isTerminalBlock(&blockError{Reason: fr}) {
			t.Errorf("finish reason %s should still be retried", finishReasonName(fr))
		}
	}
	if isTerminalBlock(errors.New("some transport failure")) {
		t.Error("a non-block error is not a terminal block")
	}
}

// The observed failure: two 18s attempts inside a 45s budget left the retry
// racing the deadline. One that cannot finish must not be started.
func TestHasBudgetForRetry(t *testing.T) {
	// 18s of a 45s budget spent: 27s left, another ~18s attempt fits.
	roomy, cancelRoomy := context.WithTimeout(context.Background(), 27*time.Second)
	defer cancelRoomy()
	if !hasBudgetForRetry(roomy, 18*time.Second) {
		t.Error("27s left is room for another 18s attempt")
	}

	// 40s of the same budget spent: 5s left, and the retry needs ~40s.
	tight, cancelTight := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelTight()
	if hasBudgetForRetry(tight, 40*time.Second) {
		t.Error("5s left is not room for another 40s attempt")
	}

	done, cancelDone := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancelDone()
	time.Sleep(5 * time.Millisecond)
	if hasBudgetForRetry(done, time.Second) {
		t.Error("an expired context has no budget")
	}

	if !hasBudgetForRetry(context.Background(), time.Hour) {
		t.Error("a context with no deadline always has budget")
	}
}

// The response shape that was silently costing every chatty generation:
// TEXT("Here's the image…") followed by the image itself. Returning on the
// first recognised part threw the image away and charged for it anyway.
func TestExtractImageFindsBlobAfterNarration(t *testing.T) {
	want := []byte{0x89, 0x50, 0x4E, 0x47}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{Content: &genai.Content{Parts: []genai.Part{
				genai.Text("Here's the image of the customer wearing the black suit. "),
				genai.Blob{MIMEType: "image/png", Data: want},
			}}},
		},
	}

	got, err := extractImage(resp, "test", "test-model", time.Second)
	if err != nil {
		t.Fatalf("narration before the image must not fail the generation: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// An unknown part type must not shadow an image that follows it either.
func TestExtractImageSkipsUnknownPartsBeforeBlob(t *testing.T) {
	want := []byte{0xFF, 0xD8}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{Content: &genai.Content{Parts: []genai.Part{
				genai.FunctionCall{Name: "unexpected"},
				genai.Blob{MIMEType: "image/jpeg", Data: want},
			}}},
		},
	}

	got, err := extractImage(resp, "test", "test-model", time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Text with no image anywhere is still a failure — never hand a sentence to
// S3 as image bytes.
func TestExtractImageTextOnlyStillFails(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{Content: &genai.Content{Parts: []genai.Part{
				genai.Text("I can't help with that."),
				genai.Text(" Sorry."),
			}}},
		},
	}

	if _, err := extractImage(resp, "test", "test-model", time.Second); err == nil ||
		!strings.Contains(err.Error(), "text instead of an image") {
		t.Fatalf("expected a text-response error, got %v", err)
	}
}
