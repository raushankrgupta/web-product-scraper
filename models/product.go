package models

// Variant represents a specific product variation
type Variant struct {
	ASIN   string   `json:"asin"`
	Size   string   `json:"size"`
	Color  string   `json:"color"`
	Images []string `json:"image_paths"`
}

// Product represents the scraped product details
type Product struct {
	Title            string    `json:"title"`
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
