package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
	"github.com/raushankrgupta/web-product-scraper/utils/alert"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// geminiTimeout / geminiMultiTimeout bound a single generation.
//
// Both used to be 5 minutes, which is how a single /try-on request stayed
// alive for 2m46s holding a connection, a goroutine and the user's attention
// before failing anyway. A generation that hasn't returned inside these
// budgets is not going to.
func geminiTimeout() time.Duration {
	return time.Duration(config.GeminiTimeoutSecs) * time.Second
}

func geminiMultiTimeout() time.Duration {
	return time.Duration(config.GeminiMultiTimeoutSecs) * time.Second
}

// respondGenError logs the real error, alerts on it, and returns the
// classified, user-safe message. Never hand genErr.Error() to a client.
//
// `overrides` lets a caller replace the copy for a specific status while
// keeping the shared classification — the guest funnel words its 422
// differently because that user has no wardrobe to fall back on.
func respondGenError(w http.ResponseWriter, r *http.Request, logger *strings.Builder,
	route, tryOnType string, genErr error, started time.Time, overrides ...map[int]string) {

	status, msg := classifyGenErr(genErr)
	for _, o := range overrides {
		if v, ok := o[status]; ok && v != "" {
			msg = v
		}
	}
	userID, _ := GetUserIDFromContext(r.Context())
	reqID := utils.RequestIDFromContext(r.Context())

	utils.AddToLogMessage(logger, fmt.Sprintf("try-on generation failed (status=%d): %v", status, genErr))
	utils.L(r.Context()).Error("try-on generation failed",
		"type", tryOnType, "status", status, "error", genErr.Error(),
		"duration_ms", float64(time.Since(started).Microseconds())/1000)

	level := alert.LevelError
	if status == http.StatusUnprocessableEntity {
		// A safety block is a content problem, not an outage.
		level = alert.LevelWarn
	}
	alert.Report(alert.Event{
		Level:     level,
		Component: "tryon",
		Title:     tryOnType + " try-on failed",
		Err:       genErr,
		RequestID: reqID,
		UserID:    userID,
		Route:     route,
		Method:    r.Method,
		Status:    status,
		Latency:   time.Since(started),
		Fields:    map[string]string{"type": tryOnType},
	})

	utils.RespondJSON(w, status, map[string]string{"error": msg})
}

// TryOnRequest represents the request body for virtual try-on
type TryOnRequest struct {
	ProductID string `json:"product_id"`
	PersonID  string `json:"person_id"`
}

// AdvancedTryOnRequest handles the unified payload mapping for all try-on variants
type AdvancedTryOnRequest struct {
	Type     string               `json:"type"` // "individual", "couple", "group"
	UseTheme bool                 `json:"use_theme"`
	ThemeID  string               `json:"theme_id"`
	People   []models.TryOnPerson `json:"people"`
}

// VirtualTryOnHandler handles the virtual try-on request
func VirtualTryOnHandler(w http.ResponseWriter, r *http.Request) {
	var logMessageBuilder strings.Builder
	defer func() {
		utils.FlushLog(r.Context(), &logMessageBuilder)
	}()
	utils.AddToLogMessage(&logMessageBuilder, "[Virtual Try-On API]")

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TryOnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Validate input
	if req.ProductID == "" || req.PersonID == "" {
		utils.RespondError(w, &logMessageBuilder, "product_id and person_id are required", http.StatusBadRequest)
		return
	}

	userIdStr, userErr := GetUserIDFromContext(r.Context())
	if userErr != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized: No user ID", http.StatusUnauthorized)
		return
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Try-On Request: PersonID=%s, ProductID=%s", req.PersonID, req.ProductID))

	// 1. Fetch Person Data (with ownership check)
	personObjID, err := primitive.ObjectIDFromHex(req.PersonID)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid person ID", http.StatusBadRequest)
		return
	}
	userObjID, _ := primitive.ObjectIDFromHex(userIdStr)

	personCollection := utils.GetCollection(config.DBName, "person")
	var person models.Person
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = personCollection.FindOne(ctx, bson.M{
		"_id":        personObjID,
		"user_id":    userObjID,
		"is_deleted": bson.M{"$ne": true},
	}).Decode(&person)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Person not found", http.StatusNotFound)
		return
	}

	if len(person.ImagePaths) == 0 {
		utils.RespondError(w, &logMessageBuilder, "Person has no images", http.StatusBadRequest)
		return
	}

	// 2. Get Product Data (from DB)
	var product models.Product
	// Fetch from database
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Fetching product from DB: %s", req.ProductID))
	productObjID, err := primitive.ObjectIDFromHex(req.ProductID)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid product ID", http.StatusBadRequest)
		return
	}

	productCollection := utils.GetCollection(config.DBName, "products")
	err = productCollection.FindOne(ctx, bson.M{"_id": productObjID}).Decode(&product)
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Product not found", http.StatusNotFound)
		return
	}
	utils.AddToLogMessage(&logMessageBuilder, "Product fetched from database")

	// Short-window result cache. A user tapping "try on" twice on the same
	// pair gets the same image back instantly and costs nothing. The key is
	// scoped to the user and is only populated after a successful,
	// already-authorised generation, so a hit can't leak another user's
	// result. The response keeps the same shape the app expects (including
	// tryon_details.product_url, which drives the "Buy" button).
	cacheKey := tryOnCacheKey(userIdStr, req.PersonID, req.ProductID, "")
	if cachedKey, ok := lookupTryOnResult(cacheKey); ok {
		if url, urlErr := utils.GetPresignedURL(r.Context(), cachedKey); urlErr == nil {
			utils.AddToLogMessage(&logMessageBuilder, "Served from recent-result cache (no Gemini call)")
			utils.L(r.Context()).Info("try-on cache hit", "user_id", userIdStr, "key", cachedKey)
			// Tell QuotaMiddleware not to bill this: we did not generate
			// anything, so it must not consume one of the user's daily
			// try-ons.
			w.Header().Set(CachedResultHeader, "1")
			utils.RespondJSON(w, http.StatusOK, map[string]interface{}{
				"result": url,
				"cached": true,
				"tryon_details": models.TryOn{
					UserID:            userIdStr,
					PersonID:          req.PersonID,
					ProductURL:        product.URL,
					ProductID:         req.ProductID,
					GeneratedImageURL: url,
					Status:            "completed",
					CreatedAt:         time.Now(),
				},
			})
			return
		}
	}

	// Pre-process Product Images: Ensure they are accessible URLs
	// We use our helper which handles checking if it's already a URL or needs presigning
	product.Images = utils.PresignImageURLs(r.Context(), product.Images)

	// 3. Call Gemini API
	// Construct person details string
	personDetails := fmt.Sprintf("Gender: %s, Height: %.2f cm, Weight: %.2f kg, Chest: %.2f, Waist: %.2f, Hips: %.2f",
		person.Gender, person.Height, person.Weight, person.Chest, person.Waist, person.Hips)

	// Use the first image of the person
	personImageKey := person.ImagePaths[0]
	personImageURL, err := utils.GetPresignedURL(r.Context(), personImageKey)
	if err != nil {
		// Log the S3/credential detail; never return it. The raw message
		// names IAM roles, IMDS endpoints and bucket internals.
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to presign person image %s: %v", personImageKey, err))
		utils.L(r.Context()).Error("presign failed", "component", "s3", "key", personImageKey, "error", err.Error())
		alert.Errorf("s3", "presign failed for person image", err)
		utils.RespondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "We couldn't load your photo. Please try again.",
		})
		return
	}

	// Route /try-on through the maintained generator.
	//
	// This endpoint used to call utils.GenerateTryOnImage — a superseded
	// function with no SafetySettings, no safety-block retry, no MIME
	// sniffing (it hardcoded "jpeg" for PNG/WebP payloads) and a prompt
	// containing "do not change the person's image with new person's image",
	// which is precisely the phrasing that trips Gemini's identity-swap
	// classifier. Every "no content generated" failure in the production log
	// came from that function, including two that ran while credits were
	// healthy. GenerateIndividualTryOnImage has all of the above; the
	// request/response shape of /try-on is unchanged so the app needs no
	// release.
	//
	// Person dimensions, which the legacy prompt passed separately, are
	// folded into the details string.
	if product.Dimensions != "" {
		personDetails += fmt.Sprintf(", Product dimensions: %s", product.Dimensions)
	}

	geminiCtx, cancelGemini := context.WithTimeout(context.Background(), geminiTimeout())
	defer cancelGemini()

	genStart := time.Now()
	generatedContent, err := utils.GenerateIndividualTryOnImage(geminiCtx, "", "", utils.PersonTryOnData{
		Details:        personDetails,
		PersonImageURL: personImageURL,
		// Legacy /try-on has no garment-slot concept — the product's images
		// are all "top" as far as the generator is concerned.
		TopURL: product.Images,
	})
	if err != nil {
		respondGenError(w, r, &logMessageBuilder, "/try-on", "legacy", err, genStart)
		return
	}
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf(
		"Try-on generated OK: bytes=%d duration=%s", len(generatedContent), time.Since(genStart).Round(time.Millisecond)))

	// 4. Save Try-On Record
	// Upload generated image to S3
	fileName := fmt.Sprintf("generated_tryon_%d.jpg", time.Now().UnixNano())
	objectKey := fmt.Sprintf("generated_images/%s", fileName)

	// generatedContent is []byte
	_, err = utils.UploadFileToS3(r.Context(), bytes.NewReader(generatedContent), objectKey, "image/jpeg")
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to upload generated image: %v", err))
		alert.Errorf("s3", "generated image upload failed", err)
		utils.RespondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "We generated your look but couldn't save it. Please try again.",
		})
		return
	}

	// Capture UserID
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, "Warning: UserID not found in context")
	}

	tryOnRecord := models.TryOn{
		ID:                primitive.NewObjectID(),
		UserID:            userID,
		PersonID:          req.PersonID,
		ProductURL:        product.URL,
		ProductID:         req.ProductID,
		PersonImageURL:    personImageKey, // Store Key
		GeneratedImageURL: objectKey,      // Store Key
		Status:            "completed",
		CreatedAt:         time.Now(),
	}

	tryOnCollection := utils.GetCollection(config.DBName, "tryons")
	_, err = tryOnCollection.InsertOne(context.Background(), tryOnRecord)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to save try-on record: %v", err))
		// The user still gets their image; but a silent write failure is why
		// the gallery could be empty for everyone with nobody noticing.
		alert.Errorf("mongo", "try-on record insert failed", err)
	}

	// Generate Presigned URL for response
	presignedGeneratedURL, _ := utils.GetPresignedURL(r.Context(), objectKey)
	tryOnRecord.GeneratedImageURL = presignedGeneratedURL

	// 5. Return Response
	response := map[string]interface{}{
		"result":        tryOnRecord.GeneratedImageURL,
		"tryon_details": tryOnRecord,
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Try-on stored OK: key=%s total=%s", objectKey, time.Since(genStart).Round(time.Millisecond)))
	utils.L(r.Context()).Info("try-on success",
		"type", "legacy", "user_id", userID, "person_id", req.PersonID, "product_id", req.ProductID,
		"bytes", len(generatedContent), "key", objectKey,
		"duration_ms", float64(time.Since(genStart).Microseconds())/1000)

	// Remember the result briefly so a double-tap returns the same image
	// instead of paying for a second identical generation.
	rememberTryOnResult(cacheKey, objectKey)

	utils.RespondJSON(w, http.StatusOK, response)
}

// IndividualTryOnHandler handles individual try-on using the unified optimized payload
func IndividualTryOnHandler(w http.ResponseWriter, r *http.Request) {
	processMultiPersonTryOn(w, r, 1, "individual")
}

// CoupleTryOnHandler handles couple try-on
func CoupleTryOnHandler(w http.ResponseWriter, r *http.Request) {
	processMultiPersonTryOn(w, r, 2, "couple")
}

// GroupTryOnHandler handles group try-on
func GroupTryOnHandler(w http.ResponseWriter, r *http.Request) {
	processMultiPersonTryOn(w, r, 0, "group") // 0 means dynamic count logic inside
}

func processMultiPersonTryOn(w http.ResponseWriter, r *http.Request, requiredPeople int, tryOnType string) {
	var logMessageBuilder strings.Builder
	defer func() { utils.FlushLog(r.Context(), &logMessageBuilder) }()
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("[%s Try-On API]", strings.ToUpper(tryOnType)))

	if r.Method != http.MethodPost {
		utils.RespondError(w, &logMessageBuilder, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AdvancedTryOnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.RespondError(w, &logMessageBuilder, "Invalid request body", http.StatusBadRequest)
		return
	}

	if requiredPeople > 0 && len(req.People) != requiredPeople {
		utils.RespondError(w, &logMessageBuilder, fmt.Sprintf("Expected %d people, got %d", requiredPeople, len(req.People)), http.StatusBadRequest)
		return
	}

	// Need userID early so we can scope person lookups to the caller (ownership
	// check) and avoid returning someone else's profile if an ID is guessed.
	userIDStr, err := GetUserIDFromContext(r.Context())
	if err != nil {
		utils.RespondError(w, &logMessageBuilder, "Unauthorized: No user ID", http.StatusUnauthorized)
		return
	}
	userObjID, _ := primitive.ObjectIDFromHex(userIDStr)

	// This context covers DB work only (theme, person, wardrobe lookups), not
	// the generation call — 30s is already generous for three indexed reads.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Process Theme

	var themeReferenceURL string
	var themeDescription string

	if req.UseTheme && req.ThemeID != "" && req.ThemeID != "null" {
		themeObjID, err := primitive.ObjectIDFromHex(req.ThemeID)
		if err != nil {
			utils.RespondError(w, &logMessageBuilder, "Invalid theme ID", http.StatusBadRequest)
			return
		}
		themeCollection := utils.GetCollection(config.DBName, "themes")
		var theme models.Theme
		if err := themeCollection.FindOne(ctx, bson.M{"_id": themeObjID}).Decode(&theme); err != nil {
			utils.RespondError(w, &logMessageBuilder, "Theme not found", http.StatusNotFound)
			return
		}

		themeDescription = theme.Description

		if theme.ThemeBlankImageURL != "" {
			themeReferenceURL, _ = utils.GetPresignedURL(r.Context(), theme.ThemeBlankImageURL)
		}
	}

	// 2. Process People
	var peopleData []utils.PersonTryOnData
	personCollection := utils.GetCollection(config.DBName, "person")
	wardrobeCollection := utils.GetCollection(config.DBName, "wardrobe")

	for _, p := range req.People {
		personObjID, err := primitive.ObjectIDFromHex(p.PersonID)
		if err != nil {
			utils.RespondError(w, &logMessageBuilder, "Invalid person ID: "+p.PersonID, http.StatusBadRequest)
			return
		}
		var person models.Person
		if err := personCollection.FindOne(ctx, bson.M{
			"_id":        personObjID,
			"user_id":    userObjID,
			"is_deleted": bson.M{"$ne": true},
		}).Decode(&person); err != nil {
			utils.RespondError(w, &logMessageBuilder, "Person not found: "+p.PersonID, http.StatusNotFound)
			return
		}

		personImgURL := ""
		if len(person.ImagePaths) > 0 {
			personImgURL, _ = utils.GetPresignedURL(r.Context(), person.ImagePaths[0])
		}

		var detailsParts []string
		if person.Gender != "" {
			detailsParts = append(detailsParts, fmt.Sprintf("Gender: %s", person.Gender))
		}
		if person.Height > 0 {
			detailsParts = append(detailsParts, fmt.Sprintf("Height: %.2f cm", person.Height))
		}
		if person.Weight > 0 {
			detailsParts = append(detailsParts, fmt.Sprintf("Weight: %.2f kg", person.Weight))
		}
		if person.Chest > 0 {
			detailsParts = append(detailsParts, fmt.Sprintf("Chest: %.2f cm", person.Chest))
		}
		if person.Waist > 0 {
			detailsParts = append(detailsParts, fmt.Sprintf("Waist: %.2f cm", person.Waist))
		}
		if person.Hips > 0 {
			detailsParts = append(detailsParts, fmt.Sprintf("Hips: %.2f cm", person.Hips))
		}
		details := strings.Join(detailsParts, ", ")

		getWardrobeImages := func(itemID string) []string {
			if itemID != "" && itemID != "null" {
				objID, err := primitive.ObjectIDFromHex(itemID)
				if err == nil {
					var item models.WardrobeItem
					if err := wardrobeCollection.FindOne(ctx, bson.M{"_id": objID}).Decode(&item); err == nil && len(item.Images) > 0 {
						item.Images = utils.PresignImageURLs(r.Context(), item.Images)
						return item.Images
					}
				}
			}
			return []string{}
		}

		topURLs := getWardrobeImages(p.TopID)
		bottomURLs := getWardrobeImages(p.BottomID)
		accessoryURLs := getWardrobeImages(p.AccessoryID)
		dressURLs := getWardrobeImages(p.DressID)

		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Person %s: TopID=%v (URL_found:%v), BottomID=%v (URL_found:%v), AccessID=%v (URL_found:%v), DressID=%v (URL_found:%v)",
			p.PersonID, p.TopID, len(topURLs) != 0, p.BottomID, len(bottomURLs) != 0, p.AccessoryID, len(accessoryURLs) != 0, p.DressID, len(dressURLs) != 0))

		peopleData = append(peopleData, utils.PersonTryOnData{
			Details:        details,
			PersonImageURL: personImgURL,
			TopURL:         topURLs,
			BottomURL:      bottomURLs,
			AccessoryURL:   accessoryURLs,
			DressURL:       dressURLs,
		})
	}

	// 3. Call Gemini API
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Calling Gemini for %s try-on with %d people", tryOnType, len(peopleData)))

	// Multi-person generation is legitimately heavier than single-person, so
	// it gets the larger of the two budgets.
	genTimeout := geminiMultiTimeout()
	if tryOnType == "individual" {
		genTimeout = geminiTimeout()
	}
	geminiCtx, cancelGemini := context.WithTimeout(context.Background(), genTimeout)
	defer cancelGemini()

	genStart := time.Now()

	var generatedContent []byte
	var genErr error

	if tryOnType == "couple" && len(peopleData) == 2 {
		generatedContent, genErr = utils.GenerateCoupleTryOnImage(geminiCtx, themeReferenceURL, themeDescription, peopleData)
	} else if tryOnType == "individual" && len(peopleData) == 1 {
		generatedContent, genErr = utils.GenerateIndividualTryOnImage(geminiCtx, themeReferenceURL, themeDescription, peopleData[0])
	} else {
		generatedContent, genErr = utils.GenerateMultiPersonTryOnImage(geminiCtx, tryOnType, themeReferenceURL, themeReferenceURL, themeDescription, peopleData)
	}
	if genErr != nil {
		respondGenError(w, r, &logMessageBuilder, "/try-on/"+tryOnType, tryOnType, genErr, genStart)
		return
	}
	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf(
		"Try-on generated OK: bytes=%d duration=%s", len(generatedContent), time.Since(genStart).Round(time.Millisecond)))

	// 4. Save Try-On Record
	fileName := fmt.Sprintf("generated_tryon_%s_%d.jpg", tryOnType, time.Now().UnixNano())
	objectKey := fmt.Sprintf("generated_images/%s", fileName)

	_, err = utils.UploadFileToS3(r.Context(), bytes.NewReader(generatedContent), objectKey, "image/jpeg")
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to upload generated image: %v", err))
		alert.Errorf("s3", "generated image upload failed", err, "type", tryOnType)
		utils.RespondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "We generated your look but couldn't save it. Please try again.",
		})
		return
	}

	tryOnRecord := models.TryOn{
		ID:                primitive.NewObjectID(),
		UserID:            userIDStr,
		Type:              tryOnType,
		ThemeID:           req.ThemeID,
		People:            req.People,
		GeneratedImageURL: objectKey, // Store S3 Key
		Status:            "completed",
		CreatedAt:         time.Now(),
	}

	tryOnCollection := utils.GetCollection(config.DBName, "tryons")
	_, err = tryOnCollection.InsertOne(context.Background(), tryOnRecord)
	if err != nil {
		utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Failed to save %s try-on record: %v", tryOnType, err))
		alert.Errorf("mongo", "try-on record insert failed", err, "type", tryOnType)
	}

	presignedGeneratedURL, _ := utils.GetPresignedURL(r.Context(), objectKey)
	tryOnRecord.GeneratedImageURL = presignedGeneratedURL

	response := map[string]interface{}{
		"result":        tryOnRecord.GeneratedImageURL,
		"tryon_details": tryOnRecord,
	}

	utils.AddToLogMessage(&logMessageBuilder, fmt.Sprintf("Try-on stored OK: key=%s total=%s", objectKey, time.Since(genStart).Round(time.Millisecond)))
	utils.L(r.Context()).Info("try-on success",
		"type", tryOnType, "user_id", userIDStr, "people", len(peopleData),
		"bytes", len(generatedContent), "key", objectKey,
		"duration_ms", float64(time.Since(genStart).Microseconds())/1000)

	utils.RespondJSON(w, http.StatusOK, response)
}
