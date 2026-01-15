package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/raushankrgupta/web-product-scraper/scrapers"
)

func main() {
	urls := []string{
		"https://amzn.in/d/8sCIA5h",
		"https://www.flipkart.com/vebnor-solid-men-round-neck-pink-t-shirt/p/itm23ecb29dccd75?pid=TSHH84X4FN4VZECF&lid=LSTTSHH84X4FN4VZECFRVSMFH&marketplace=FLIPKART&store=clo%2Fash%2Fank%2Fedy&srno=b_1_16&otracker=browse&fm=organic&iid=4a9f5d59-576c-42e4-83da-cceb91bc75e4.TSHH84X4FN4VZECF.SEARCH&ppt=browse&ppn=browse&ssid=zg8rl62fzk0000001768474667123",
		"https://www.myntra.com/tshirts/h%26m/hm-men-white-solid-cotton-pure-cotton-t-shirt-regular-fit/11468714/buy",
		"https://www.tatacliq.com/thomas-scott-black-regular-fit-checks-shirt/p-mp000000027887447",
		"https://peterengland.abfrl.in/p/men-blue-slim-fit-shirt-39903346.html?source=plp",
	}

	for _, u := range urls {
		fmt.Printf("Testing URL: %s\n", u)
		scraper, resolved, err := scrapers.GetScraper(u)
		if err != nil {
			log.Printf("Failed to get scraper for %s: %v\n", u, err)
			continue
		}
		fmt.Printf("Resolved URL: %s\n", resolved)
		fmt.Printf("Scraper: %T\n", scraper)

		product, err := scraper.ScrapeProduct(resolved)
		if err != nil {
			log.Printf("Failed to scrape product: %v\n", err)
			continue
		}

		b, _ := json.MarshalIndent(product, "", "  ")
		fmt.Printf("Product: %s\n", string(b))
		fmt.Println("--------------------------------------------------")
	}
}
