package utils

import (
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
	if !strings.Contains(err.Error(), "Safety") {
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
