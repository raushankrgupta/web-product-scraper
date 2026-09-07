package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Upload limits. The body cap predates link import; the file-count cap and
// the per-user rate were added with it, because a feature that turns any web
// page into an upload with one tap needs a ceiling that "pick from gallery"
// never did.
const (
	maxUploadFiles          = 8
	uploadRatePerUser       = 60
	uploadRateWindow        = time.Hour
	maxImportTitleLen       = 200
	defaultUploadTitle      = "User Uploaded Product"
	defaultImportTitle      = "Imported product"
	productSourceUserUpload = "user_upload"
	productSourceLinkImport = "link_import"
)

// uploadLimiter caps /product/upload per user id.
var uploadLimiter = newKeyedLimiter()

// importMethods are the values a client may report for how it obtained the
// images. Anything else is dropped (not rejected): it is analytics, and an
// unknown label should never fail a user's upload.
var importMethods = map[string]bool{
	"dom_pick": true, "tap": true, "in_page_fetch": true, "screenshot": true, "mixed": true,
}

// importMeta is the optional provenance a client may attach to an upload.
// Legacy clients send none of it and get the user_upload defaults.
type importMeta struct {
	Source       string
	SourceURL    string // cleaned by utils.ValidateReferenceURL; "" for user_upload
	Title        string
	ImportMethod string
	PageHost     string
}

// errInvalidImportMeta is returned with a client-safe message.
type errInvalidImportMeta struct{ msg string }

func (e *errInvalidImportMeta) Error() string { return e.msg }

// parseImportMeta reads the optional link-import fields from a parsed
// multipart form. The rules are the contract in
// fitly-app/docs/USER_SIDE_LINK_IMPORT_PLAN.md §7.2:
//
//   - source: "user_upload" (default) | "link_import"; anything else rejected
//   - source_url: required for link_import; stored, never fetched
//   - title: <= 200 chars, control characters stripped
//   - import_method: allow-listed label or dropped
//   - page_host: lower-cased; derived from source_url when absent
func parseImportMeta(form func(string) string) (importMeta, error) {
	meta := importMeta{Source: productSourceUserUpload, Title: defaultUploadTitle}

	switch src := strings.ToLower(strings.TrimSpace(form("source"))); src {
	case "", productSourceUserUpload:
		// Legacy or gallery upload — defaults stand. A source_url on a plain
		// upload is ignored rather than stored: the user did not import from
		// a page, so recording one would be a false provenance claim.
	case productSourceLinkImport:
		meta.Source = productSourceLinkImport
		meta.Title = defaultImportTitle
		cleaned, err := utils.ValidateReferenceURL(form("source_url"))
		if err != nil {
			return meta, &errInvalidImportMeta{"source_url is required for a link import and must be an http(s) URL"}
		}
		meta.SourceURL = cleaned
		meta.PageHost = utils.HostOfURL(cleaned)
	default:
		return meta, &errInvalidImportMeta{"invalid source"}
	}

	if t := sanitizeTitle(form("title")); t != "" {
		meta.Title = t
	}
	if m := strings.ToLower(strings.TrimSpace(form("import_method"))); importMethods[m] {
		meta.ImportMethod = m
	}
	if h := strings.ToLower(strings.TrimSpace(form("page_host"))); h != "" && meta.Source == productSourceLinkImport {
		// Prefer what we derived from the validated URL; the client's value
		// only fills a gap (e.g. the URL had no parseable host, which
		// ValidateReferenceURL already rejects — so in practice this is a no-op
		// guard against a future relaxation).
		if meta.PageHost == "" && len(h) <= 253 {
			meta.PageHost = strings.TrimPrefix(h, "www.")
		}
	}
	return meta, nil
}

// sanitizeTitle trims, strips control characters and collapses internal
// whitespace, then truncates to maxImportTitleLen runes.
func sanitizeTitle(raw string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range raw {
		// Whitespace first: a tab is both IsSpace and IsControl, and it should
		// become a space, not vanish and glue two words together.
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteRune(' ')
			}
			lastSpace = true
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		lastSpace = false
		b.WriteRune(r)
	}
	t := strings.TrimSpace(b.String())
	if rs := []rune(t); len(rs) > maxImportTitleLen {
		t = strings.TrimSpace(string(rs[:maxImportTitleLen]))
	}
	return t
}

// UploadProductHandler handles the upload of product images by users.
//
// Two callers share it: the gallery/camera path (since forever) and, from app
// 2.4.0, Link Import — images the user picked in an in-app browser on their
// own phone. The wire format is identical; the second caller merely adds the
// optional provenance fields read by parseImportMeta. That is deliberate: the
// server treats an imported image exactly like a gallery photo because, as
// far as the server is concerned, it is one.
func UploadProductHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Upload Product API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userIdStr, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized: No user ID", http.StatusUnauthorized)
		return
	}

	// Checked before the body is read so a limited client does not pay for
	// an upload we are about to refuse.
	if !uploadLimiter.allow(userIdStr, uploadRatePerUser, uploadRateWindow) {
		utils.L(r.Context()).Warn("product upload rate limited", "user_id", userIdStr)
		utils.RespondErrorReason(w, &logMessageBuilder,
			"You're importing very quickly. Please wait a few minutes.", "rate_limited", http.StatusTooManyRequests)
		return
	}

	// Enforce strict upload limit (15MB) to prevent disk exhaustion DoS
	r.Body = http.MaxBytesReader(w, r.Body, 15<<20)

	// Parse multipart form (max 10MB in RAM)
	err = r.ParseMultipartForm(10 << 20)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Error parsing form data: upload exceeds maximum allowed limit", http.StatusBadRequest)
		return
	}

	files := r.MultipartForm.File["images"]
	if len(files) == 0 {
		utils.RespondError(w, &logMessageBuilder, "No images uploaded", http.StatusBadRequest)
		return
	}
	if len(files) > maxUploadFiles {
		utils.RespondErrorReason(w, &logMessageBuilder,
			fmt.Sprintf("Too many images (max %d)", maxUploadFiles), "too_many_images", http.StatusBadRequest)
		return
	}

	meta, err := parseImportMeta(r.FormValue)
	if err != nil {
		var bad *errInvalidImportMeta
		if errors.As(err, &bad) {
			utils.RespondErrorReason(w, &logMessageBuilder, bad.msg, "invalid_request", http.StatusBadRequest)
			return
		}
		utils.RespondError(w, &logMessageBuilder, "Invalid request", http.StatusBadRequest)
		return
	}

	imagePaths := storeUploadedImages(r.Context(), files)
	if len(imagePaths) == 0 {
		utils.RespondError(w, &logMessageBuilder, "Failed to upload any images", http.StatusInternalServerError)
		return
	}

	product := models.Product{
		UserID:       userIdStr,
		Source:       meta.Source,
		URL:          meta.SourceURL,
		Title:        meta.Title,
		Images:       imagePaths,
		CreatedAt:    time.Now(),
		ImportMethod: meta.ImportMethod,
		PageHost:     meta.PageHost,
		// Initialize other fields as empty/default to avoid nil pointer issues if used elsewhere
		Dimensions: "",
		Category:   "User Upload",
	}
	if meta.Source == productSourceLinkImport {
		// Never store retailer copy. The description is a literary work in
		// its own right and nothing downstream needs it.
		product.Description = ""
	} else {
		product.Description = "Uploaded by user"
	}

	collection := utils.GetCollection(config.DBName, "products")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := collection.InsertOne(ctx, product)
	if err != nil {
		utils.RespondInternalError(w, r, &logMessageBuilder, "mongo",
			"We couldn't save that product. Please try again.", err, http.StatusInternalServerError)
		return
	}
	product.ID = result.InsertedID.(primitive.ObjectID)

	// Hostname and counts only — never the URL's query string, which on
	// retailer links carries session and campaign identifiers.
	utils.L(r.Context()).Info("product uploaded",
		"source", product.Source, "host", product.PageHost, "images", len(imagePaths),
		"method", product.ImportMethod, "client_version", r.Header.Get("X-App-Version"))

	// Presign URLs for immediate display
	product.Images = utils.PresignImageURLs(r.Context(), product.Images)

	utils.RespondJSON(w, http.StatusCreated, product)
}

// storeUploadedImages validates each file by magic bytes and uploads it under
// product_uploads/. Invalid or failed files are skipped, not fatal — a partial
// product beats none, and the caller checks for zero.
func storeUploadedImages(ctx context.Context, files []*multipart.FileHeader) []string {
	var imagePaths []string
	for _, fileHeader := range files {
		file, err := fileHeader.Open()
		if err != nil {
			continue
		}

		func() {
			defer file.Close()
			objectKey, mime, err := utils.ValidateImageFile(file, "product_uploads")
			if err != nil {
				slog.Warn("rejected invalid product image upload", "filename", fileHeader.Filename, "error", err)
				return
			}

			_, err = utils.UploadFileToS3(ctx, file, objectKey, mime)
			if err != nil {
				slog.Info(fmt.Sprintf("Failed to upload %s: %v", objectKey, err))
				return
			}
			imagePaths = append(imagePaths, objectKey)
		}()
	}
	return imagePaths
}
