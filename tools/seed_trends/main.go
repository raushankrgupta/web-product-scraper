// Command seed_trends inserts the initial example trends into MongoDB.
//
// Seeds:
//  1. 1980s Retro Movie Star
//  2. 1980s Family Album
//  3. VHS Camcorder Still
//  4. Ghibli Style
//  5. Disney Character
//  6. Water color
//
// Usage:
//
//	go run ./tools/seed_trends               # inserts as active
//	go run ./tools/seed_trends -status draft # inserts as draft
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/raushankrgupta/web-product-scraper/config"
	"github.com/raushankrgupta/web-product-scraper/models"
	"github.com/raushankrgupta/web-product-scraper/utils"
)

func main() {
	status := flag.String("status", "active", "initial status (active|draft)")
	flag.Parse()

	config.LoadConfig()
	if err := utils.ConnectMongo(config.MongoURI); err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if utils.Client != nil {
			_ = utils.Client.Disconnect(ctx)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	coll := utils.GetCollection(config.DBName, models.CollTrends)

	seeds := []models.Trend{
		{
			Slug:         "1980s-retro-movie-star",
			Version:      1,
			Title:        "1980s Retro Movie Star",
			Subtitle:     "Hand-painted Bollywood poster",
			Description:  "Star in your own 1980s cinema poster with authentic hand-painted aesthetic, analog film grain and period styling.",
			Badge:        "HOT",
			AccentColor:  "#e11d48",
			Category:     "Vintage",
			SortOrder:    10,
			Status:       *status,
			Environments: []string{"local", "qa", "prod"},
			Inputs: models.TrendInputs{
				People: models.PeopleInput{
					Enabled:        true,
					Sources:        []string{"profile", "upload"},
					Min:            1,
					Max:            2,
					AllowAddPerson: true,
					RequireFace:    true,
					Label:          "Choose Lead Star(s)",
					RoleLabels:     []string{"Lead star", "Co-star"},
				},
				Background: models.BackgroundInput{
					Enabled:     true,
					Modes:       []string{"none", "text"},
					DefaultMode: "none",
					Label:       "Poster Background / Setting",
					Help:        "Describe the scene (e.g. neon Mumbai street, burning car, dramatic thunderstorm)",
				},
				Pose: models.TextInput{
					Enabled:     true,
					Label:       "Pose",
					Placeholder: "e.g. dramatic heroic stare, holding vintage sunglasses",
				},
				Instruction: models.TextInput{
					Enabled:     true,
					Label:       "Custom details",
					Placeholder: "e.g. 1980s aviators, leather jacket, dramatic wind in hair",
				},
				Fields: []models.CustomField{
					{
						Key:            "movie_title",
						Label:          "Movie Title",
						Help:           "Appears as bold hand-painted title on the poster",
						Type:           models.FieldTypeText,
						MaxChars:       40,
						PromptFragment: "The poster's title text reads \"{{value}}\" in bold hand-painted vintage cinema lettering.",
					},
					{
						Key:     "era",
						Label:   "Era",
						Type:    models.FieldTypeSingle,
						Default: "1985",
						Choices: []models.FieldChoice{
							{Value: "1975", Label: "Mid 70s", PromptFragment: "mid-1970s Bollywood cinema poster styling"},
							{Value: "1985", Label: "Mid 80s", PromptFragment: "mid-1980s Bollywood cinema poster styling"},
							{Value: "1992", Label: "Early 90s", PromptFragment: "early-1990s Bollywood cinema poster styling"},
						},
					},
					{
						Key:            "grain",
						Label:          "Heavy Analog Grain",
						Type:           models.FieldTypeBoolean,
						Default:        true,
						PromptFragment: "Add heavy analog film grain and faded vintage cinema offset-ink registration.",
					},
				},
				Quality: models.QualityInput{
					Enabled: true,
					Allowed: []string{"flash", "pro"},
					Default: "flash",
				},
				Outputs: models.CountInput{
					Enabled: true,
					Min:     1,
					Max:     2,
					Default: 1,
				},
				Aspect: models.ChoiceInput{
					Enabled: true,
					Default: "portrait",
					Choices: []models.FieldChoice{
						{Value: "portrait", Label: "Poster 4:5", PromptFragment: "Vertical movie poster composition with aspect ratio 4:5."},
						{Value: "story", Label: "Story 9:16", PromptFragment: "Vertical full-screen cinema poster composition with aspect ratio 9:16."},
					},
				},
			},
			Generation: models.TrendGeneration{
				Provider:       "gemini",
				QualityTier:    "flash",
				PromptTemplate: "Create a complete fictional 1980s Bollywood movie poster featuring the person(s) in the reference photo as the lead star(s). Preserve facial identity and expressions accurately. Style with authentic 1980s hairstyles, clothing, and a hand-painted cinema aesthetic with analog film grain and faded ink. {{choice_fragments}} {{fields.movie_title}} {{pose}} {{background_text}} {{instruction}}",
				TersePrompt:    "1980s Bollywood movie poster hand-painted cinema style portrait of person in photo.",
				IdentityGuard:  true,
			},
			Pricing: models.TrendPricing{
				Mode:                models.PricingDynamic,
				BaseStars:           20,
				IncludedPeople:      1,
				PerPersonStars:      15,
				PerExtraImageStars:  5,
				PerExtraOutputStars: 15,
				QualityMultiplier: map[string]float64{
					"flash": 1.0,
					"pro":   2.0,
				},
				MinStars: 20,
				MaxStars: 80,
			},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			Slug:         "1980s-family-album",
			Version:      1,
			Title:        "1980s Family Album",
			Subtitle:     "Warm sepia nostalgia",
			Description:  "Turn your photo into an old family album picture from the 1980s with natural expressions, period clothing, and a warm faded finish.",
			Badge:        "NEW",
			AccentColor:  "#d97706",
			Category:     "Vintage",
			SortOrder:    20,
			Status:       *status,
			Environments: []string{"local", "qa", "prod"},
			Inputs: models.TrendInputs{
				People: models.PeopleInput{
					Enabled:        true,
					Sources:        []string{"profile", "upload"},
					Min:            1,
					Max:            4,
					AllowAddPerson: true,
					RequireFace:    true,
					Label:          "Select Family Members / Photos",
				},
				Background: models.BackgroundInput{
					Enabled:     true,
					Modes:       []string{"none", "text"},
					DefaultMode: "none",
					Label:       "Setting",
					Help:        "e.g. living room with vintage sofa, park picnic, garden veranda",
				},
				Pose: models.TextInput{
					Enabled:     true,
					Label:       "Pose",
					Placeholder: "e.g. sitting together on a vintage sofa, smiling warmly",
				},
				Instruction: models.TextInput{
					Enabled:     true,
					Label:       "Notes",
					Placeholder: "e.g. vintage knit sweater, warm daylight",
				},
			},
			Generation: models.TrendGeneration{
				Provider:       "gemini",
				QualityTier:    "flash",
				PromptTemplate: "Turn this photo into an authentic vintage family album photograph from the 1980s. Keep face, natural identity and expression accurately preserved. Use everyday period 1980s clothing, natural soft lighting, warm sepia tones, and a slightly faded matte photograph print finish. {{pose}} {{background_text}} {{instruction}}",
				TersePrompt:    "1980s warm sepia vintage family photograph of the subject.",
				IdentityGuard:  true,
			},
			Pricing: models.TrendPricing{
				Mode:                models.PricingDynamic,
				BaseStars:           20,
				IncludedPeople:      1,
				PerPersonStars:      10,
				PerExtraImageStars:  5,
				PerExtraOutputStars: 15,
				MinStars:            20,
				MaxStars:            60,
			},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			Slug:         "vhs-camcorder-still",
			Version:      1,
			Title:        "VHS Camcorder Still",
			Subtitle:     "Analog tape artifact aesthetic",
			Description:  "Make your photo look like a still frame from a home video recorded on a VHS camcorder in the late 1980s with scan lines and color bleeding.",
			Badge:        "",
			AccentColor:  "#8b5cf6",
			Category:     "Retro",
			SortOrder:    30,
			Status:       *status,
			Environments: []string{"local", "qa", "prod"},
			Inputs: models.TrendInputs{
				People: models.PeopleInput{
					Enabled:        true,
					Sources:        []string{"profile", "upload"},
					Min:            1,
					Max:            2,
					AllowAddPerson: true,
					RequireFace:    true,
					Label:          "Choose Photo",
				},
				Background: models.BackgroundInput{
					Enabled:     true,
					Modes:       []string{"none", "text"},
					DefaultMode: "none",
					Label:       "Setting",
				},
				Instruction: models.TextInput{
					Enabled:     true,
					Label:       "Camcorder timestamp or location note",
					Placeholder: "e.g. SUMMER '89 timestamp in bottom corner",
				},
			},
			Generation: models.TrendGeneration{
				Provider:       "gemini",
				QualityTier:    "flash",
				PromptTemplate: "Make this photo look like an authentic frozen frame from a home video recorded on a consumer VHS camcorder in the late 1980s. Add subtle horizontal scan lines, analog tape noise, slight color bleeding, soft focus, chromatic aberration, and authentic tape playback artifacts. Keep subject's face identity natural. {{background_text}} {{instruction}}",
				TersePrompt:    "Late 1980s VHS tape frame capture of the person in the photo with analog video noise.",
				IdentityGuard:  true,
			},
			Pricing: models.TrendPricing{
				Mode:      models.PricingFlat,
				FlatStars: 20,
			},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			Slug:         "ghibli-style",
			Version:      1,
			Title:        "Ghibli Style Art",
			Subtitle:     "Anime watercolour wonder",
			Description:  "Turn your photo into hand-painted Studio Ghibli style anime concept art with lush background landscapes and whimsical lighting.",
			Badge:        "HOT",
			AccentColor:  "#10b981",
			Category:     "Artistic",
			SortOrder:    40,
			Status:       *status,
			Environments: []string{"local", "qa", "prod"},
			Inputs: models.TrendInputs{
				People: models.PeopleInput{
					Enabled:        true,
					Sources:        []string{"profile", "upload"},
					Min:            1,
					Max:            2,
					AllowAddPerson: true,
					RequireFace:    true,
					Label:          "Choose Photo",
				},
				Background: models.BackgroundInput{
					Enabled:     true,
					Modes:       []string{"none", "text"},
					DefaultMode: "none",
					Label:       "Scenery / World",
					Help:        "e.g. flying island, lush green rolling hills with puffy clouds, cozy bakery",
				},
				Instruction: models.TextInput{
					Enabled:     true,
					Label:       "Whimsical detail",
					Placeholder: "e.g. gentle breeze, magical sparkles, glowing lantern",
				},
			},
			Generation: models.TrendGeneration{
				Provider:       "gemini",
				QualityTier:    "flash",
				PromptTemplate: "Turn the person in this photo into a character painted in the iconic Studio Ghibli anime feature film art style. Hand-painted gouache and watercolour aesthetic, lush vibrant scenery, painterly clouds and skies, warm nostalgic whimsical lighting. Accurately reflect the person's hair, facial structure, and expression in the animated style. {{background_text}} {{instruction}}",
				TersePrompt:    "Studio Ghibli hand-painted anime style portrait of person in photo.",
				IdentityGuard:  true,
			},
			Pricing: models.TrendPricing{
				Mode:      models.PricingFlat,
				FlatStars: 20,
			},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			Slug:         "disney-character",
			Version:      1,
			Title:        "Disney 3D Character",
			Subtitle:     "Modern 3D animated hero",
			Description:  "Transform yourself into a charming Disney / Pixar style 3D animated movie character with expressive eyes and cinematic lighting.",
			Badge:        "",
			AccentColor:  "#3b82f6",
			Category:     "Artistic",
			SortOrder:    50,
			Status:       *status,
			Environments: []string{"local", "qa", "prod"},
			Inputs: models.TrendInputs{
				People: models.PeopleInput{
					Enabled:        true,
					Sources:        []string{"profile", "upload"},
					Min:            1,
					Max:            2,
					AllowAddPerson: true,
					RequireFace:    true,
					Label:          "Choose Photo",
				},
				Background: models.BackgroundInput{
					Enabled:     true,
					Modes:       []string{"none", "text"},
					DefaultMode: "none",
					Label:       "World / Backdrop",
				},
				Instruction: models.TextInput{
					Enabled:     true,
					Label:       "Expression / Character role",
					Placeholder: "e.g. playful adventurous smirk, magical apprentice",
				},
			},
			Generation: models.TrendGeneration{
				Provider:       "gemini",
				QualityTier:    "flash",
				PromptTemplate: "Transform the person in the photo into a modern Disney Pixar 3D animated feature film character. Keep the person's facial likeness, hair colour, style, and distinctive features instantly recognisable in 3D stylized animation. Beautiful subsurface scattering skin, highly detailed hair shaders, expressive warm eyes, cinematic volumetric rim lighting. {{background_text}} {{instruction}}",
				TersePrompt:    "Modern 3D animated Disney Pixar style character portrait preserving subject's likeness.",
				IdentityGuard:  true,
			},
			Pricing: models.TrendPricing{
				Mode:      models.PricingFlat,
				FlatStars: 20,
			},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			Slug:         "watercolor-portrait",
			Version:      1,
			Title:        "Water Colour Portrait",
			Subtitle:     "Loose wet-on-wet fine art",
			Description:  "Turn your photo into a delicate, handcrafted watercolour painting with soft pastel washes, organic drips, and visible paper grain.",
			Badge:        "",
			AccentColor:  "#06b6d4",
			Category:     "Artistic",
			SortOrder:    60,
			Status:       *status,
			Environments: []string{"local", "qa", "prod"},
			Inputs: models.TrendInputs{
				People: models.PeopleInput{
					Enabled:        true,
					Sources:        []string{"profile", "upload"},
					Min:            1,
					Max:            2,
					AllowAddPerson: true,
					RequireFace:    true,
					Label:          "Choose Photo",
				},
				Instruction: models.TextInput{
					Enabled:     true,
					Label:       "Color palette notes",
					Placeholder: "e.g. soft pastel rose and lavender, golden sunlight washes",
				},
			},
			Generation: models.TrendGeneration{
				Provider:       "gemini",
				QualityTier:    "flash",
				PromptTemplate: "Create a delicate fine art watercolour painting portrait of the person in the reference photo. Preserving facial likeness and identity with soft loose wet-on-wet pigment blooms, raw cold-pressed watercolor paper texture, artistic pigment splatters and transparent color layering. {{instruction}}",
				TersePrompt:    "Loose wet-on-wet fine art watercolor portrait of person.",
				IdentityGuard:  true,
			},
			Pricing: models.TrendPricing{
				Mode:      models.PricingFlat,
				FlatStars: 20,
			},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}

	for _, trend := range seeds {
		filter := bson.M{"slug": trend.Slug}
		update := bson.M{
			// Only on insert, so re-running the seed does not reset a trend's
			// age or wipe the run count it has accumulated since.
			"$setOnInsert": bson.M{
				"_id":        primitive.NewObjectID(),
				"created_at": time.Now(),
				"run_count":  0,
			},
			"$set": bson.M{
				"slug":         trend.Slug,
				"version":      trend.Version,
				"title":        trend.Title,
				"subtitle":     trend.Subtitle,
				"description":  trend.Description,
				"badge":        trend.Badge,
				"accent_color": trend.AccentColor,
				"category":     trend.Category,
				"sort_order":   trend.SortOrder,
				"status":       trend.Status,
				"environments": trend.Environments,
				"inputs":       trend.Inputs,
				"generation":   trend.Generation,
				"pricing":      trend.Pricing,
				"updated_at":   time.Now(),
			},
		}

		res, err := coll.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
		if err != nil {
			log.Printf("Failed to seed trend %s: %v", trend.Slug, err)
			continue
		}

		if res.UpsertedCount > 0 {
			fmt.Printf("✓ Created trend: %s (%s)\n", trend.Title, trend.Slug)
		} else {
			fmt.Printf("• Updated trend: %s (%s)\n", trend.Title, trend.Slug)
		}
	}

	fmt.Println("\nAll 6 example trends seeded successfully!")
}
