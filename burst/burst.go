package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	baseURL     = flag.String("url", "http://localhost:8080", "Base URL of the API")
	concurrency = flag.Int("c", 20000, "Number of concurrent requests")
	numSeats    = flag.Int("s", 10, "Number of hot seats to fight over")
)

func main() {
	flag.Parse()

	log.Printf("Starting burst test against %s", *baseURL)
	log.Printf("Concurrency: %d, Hot Seats: %d", *concurrency, *numSeats)

	// 1. Create a show
	seats := make([]string, *numSeats)
	for i := 0; i < *numSeats; i++ {
		seats[i] = fmt.Sprintf("A%d", i)
	}

	showReq := map[string]interface{}{
		"name":        "burst-test-show",
		"price_paise": 25000,
		"seats":       seats,
	}
	body, _ := json.Marshal(showReq)

	resp, err := http.Post(*baseURL+"/shows", "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Fatalf("Failed to create show: %v", err)
	}

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		log.Fatalf("Failed to create show. Status: %d, Body: %s", resp.StatusCode, string(b))
	}

	var showRes map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&showRes)
	showID := showRes["id"].(string)
	resp.Body.Close()

	log.Printf("Created show: %s", showID)

	// 2. Fire burst
	var wg sync.WaitGroup
	results := make(chan int, *concurrency)

	start := time.Now()

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func(reqNum int) {
			defer wg.Done()

			// Distribute requests across hot seats, some users trying to grab same seats
			targetSeat := seats[reqNum%*numSeats]
			userID := fmt.Sprintf("user-%d", reqNum%5000) // 5000 distinct users
			idempKey := uuid.New().String()

			reqBody := map[string]interface{}{
				"seats": []string{targetSeat},
			}
			b, _ := json.Marshal(reqBody)

			req, _ := http.NewRequest("POST", fmt.Sprintf("%s/shows/%s/reserve", *baseURL, showID), bytes.NewBuffer(b))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-User-ID", userID)
			req.Header.Set("Idempotency-Key", idempKey)

			res, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- 0 // Map to 0 for error
				return
			}
			defer res.Body.Close()
			results <- res.StatusCode
		}(i)
	}

	wg.Wait()
	close(results)
	duration := time.Since(start)

	// 3. Collect results
	statusCounts := make(map[int]int)
	for code := range results {
		statusCounts[code]++
	}

	log.Printf("Burst completed in %v", duration)
	log.Println("Results:")
	for code, count := range statusCounts {
		log.Printf("  HTTP %d: %d", code, count)
	}

	// 4. Verify invariant
	log.Println("Verifying reconciliation invariant...")
	resp, err = http.Get(fmt.Sprintf("%s/shows/%s", *baseURL, showID))
	if err != nil {
		log.Fatalf("Failed to get show state: %v", err)
	}
	defer resp.Body.Close()

	var state map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&state)

	counts := state["counts"].(map[string]interface{})
	available := int(counts["available"].(float64))
	held := int(counts["held"].(float64))
	confirmed := int(counts["confirmed"].(float64))

	log.Printf("State: Available: %d, Held: %d, Confirmed: %d", available, held, confirmed)

	if available+held+confirmed == *numSeats {
		log.Println("Invariant OK: available + held + confirmed == total_seats")
	} else {
		log.Fatalf("Invariant FAILED! Expected %d, got %d", *numSeats, available+held+confirmed)
	}
}
