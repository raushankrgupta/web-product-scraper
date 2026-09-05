package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Variant represents a specific product variation
type Variant struct {
	ASIN   string   `json:"asin"`
	Size   string   `json:"size"`
	Color  string   `json:"color"`
	Images []string `json:"image_paths"`
}

// Product represents the scraped product details
type Product struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID      string             `bson:"user_id" json:"user_id"`
	Source      string             `bson:"source" json:"source"` // "link" or "user_upload"
	URL         string             `bson:"url" json:"url"`       // Original product URL (optional if user_upload)
	ResolvedURL string             `bson:"resolved_url,omitempty" json:"resolved_url,omitempty"`
	Status      string             `bson:"status" json:"status"`                                 // "success", "failed"
	ScrapeError string             `bson:"scrape_error,omitempty" json:"scrape_error,omitempty"` // Error details when scraping fails

	// FailureReason is the stable code the app branches on
	// (invalid_url | unsupported_site | scrape_failed) — the same value sent
	// to the client as `reason`. ScrapeError is prose that changes whenever
	// an upstream library rewords itself; this is what "which domains are we
	// losing users on?" is grouped by. Empty on a successful scrape.
	FailureReason string `bson:"failure_reason,omitempty" json:"-"`
	// FailureHost is the domain the link pointed at, kept separately so the
	// digest can group by site without parsing URLs at query time.
	FailureHost string `bson:"failure_host,omitempty" json:"-"`
	// FailureAdapter names the scraper that was selected, or "" when routing
	// itself failed. Distinguishes "we have no adapter" from "our adapter
	// broke", which are different fixes.
	FailureAdapter string `bson:"failure_adapter,omitempty" json:"-"`
	// Flow is "app" or "guest". A guest failure is a lost first impression
	// and is worth weighting differently.
	Flow string `bson:"flow,omitempty" json:"-"`

	CreatedAt        time.Time `bson:"created_at" json:"created_at"`
	Title            string    `json:"title" bson:"title"`
	MRP              string    `json:"mrp"`              // Maximum Retail Price (List Price)
	DiscountedPrice  string    `json:"discounted_price"` // Selling Price
	Discount         string    `json:"discount"`
	Description      string    `json:"description"`
	Category         string    `json:"category"`
	Subcategory      string    `json:"subcategory"`
	Dimensions       string    `json:"dimensions"`
	Material         string    `json:"material"`
	FitType          string    `json:"fit_type"`
	Images           []string  `json:"image_paths"`        // Main product images
	CurrentSelection *Variant  `json:"current_selection"`  // Details of the currently selected variant
	Variants         []Variant `json:"variants,omitempty"` // All variants (hidden if empty)
}
