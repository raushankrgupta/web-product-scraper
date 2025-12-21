package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// downloadImages downloads a list of image URLs to the specified folder
// and returns a map of URL -> Local Path
func downloadImages(urls []string, folder string) (map[string]string, error) {
	urlToPath := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup

	if err := os.MkdirAll(folder, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %v", err)
	}

	// Limit concurrency to avoid being blocked
	semaphore := make(chan struct{}, 5)

	for i, url := range urls {
		if url == "" {
			continue
		}
		wg.Add(1)
		go func(i int, url string) {
			defer wg.Done()
			semaphore <- struct{}{}        // Acquire token
			defer func() { <-semaphore }() // Release token

			// Extract filename from URL or generate one
			filename := filepath.Base(url)
			if strings.Contains(filename, "?") {
				filename = strings.Split(filename, "?")[0]
			}
			if filename == "" || len(filename) > 255 {
				filename = fmt.Sprintf("image_%d.jpg", i)
			}
			// ensure unique names if multiple have same name (unlikely but possible with weird URLs)
			filename = fmt.Sprintf("%d_%s", i, filename)

			path := filepath.Join(folder, filename)

			if err := downloadFile(url, path); err != nil {
				// We log error but don't fail the whole process for one image
				fmt.Printf("Failed to download %s: %v\n", url, err)
				return
			}

			mu.Lock()
			// Return absolute path so it's clear
			absPath, err := filepath.Abs(path)
			if err == nil {
				urlToPath[url] = absPath
			} else {
				urlToPath[url] = path
			}
			mu.Unlock()
		}(i, url)
	}

	wg.Wait()
	return urlToPath, nil
}

func downloadFile(url, filepath string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.114 Safari/537.36")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}
