package main

// Variant represents a specific product variation
type Variant struct {
	ASIN   string   `json:"asin"`
	Size   string   `json:"size"`
	Color  string   `json:"color"`
	Images []string `json:"image_paths"`
}

// Product represents the scraped product details
type Product struct {
	Title       string    `json:"title"`
	Price       string    `json:"price"`
	Discount    string    `json:"discount"`
	Description string    `json:"description"`
	Images      []string  `json:"image_paths"` // Main product images
	Variants    []Variant `json:"variants"`    // All variants
}
