package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	baseURL     = flag.String("url", "http://localhost:8080", "Base URL of the API")
	concurrency = flag.Int("c", 20000, "Total number of hot-seat requests in the burst wave")
	workers     = flag.Int("workers", 0, "Maximum hot-seat requests in flight (0 uses a platform default)")
	userCount   = flag.Int("u", 5000, "Number of distinct bearer-token users in the hot-seat storm")
	seatCount   = flag.Int("s", 10, "Number of seats to create on the test show")
)

type outcome struct {
	status int
	body   []byte
	err    error
}

type distribution struct {
	held201             int
	seatTaken           int
	perUserLimit        int
	idempotencyRejected int
	other4xx            int
	fiveXX              int
	transportErrors     int
}

func (d *distribution) add(result outcome) {
	if result.err != nil {
		d.transportErrors++
		return
	}
	switch {
	case result.status == http.StatusCreated:
		d.held201++
	case result.status == http.StatusConflict:
		body := string(result.body)
		switch {
		case strings.Contains(body, "Seat already taken"):
			d.seatTaken++
		case strings.Contains(body, "Per-user limit"):
			d.perUserLimit++
		case strings.Contains(body, "Idempotency key reused"):
			d.idempotencyRejected++
		default:
			d.other4xx++
		}
	case result.status >= 500:
		d.fiveXX++
	case result.status >= 400:
		d.other4xx++
	default:
		d.other4xx++
	}
}

func (d distribution) print(label string) {
	fmt.Printf("%s: 201-held=%d 409-seat-taken=%d 409-per-user-limit=%d 409-idempotency-replay=%d other-4xx=%d 5xx=%d transport-errors=%d\n",
		label, d.held201, d.seatTaken, d.perUserLimit, d.idempotencyRejected, d.other4xx, d.fiveXX, d.transportErrors)
}

func doJSON(client *http.Client, method, url string, body any, bearer string) (int, []byte, error) {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	responseBytes, err := io.ReadAll(response.Body)
	return response.StatusCode, responseBytes, err
}

func registerTokens(client *http.Client, count int) ([]string, error) {
	tokens := make([]string, count)
	type result struct {
		index int
		token string
		err   error
	}
	jobs := make(chan int)
	results := make(chan result, count)
	workers := 64
	if count < workers {
		workers = count
	}
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				status, body, err := doJSON(client, http.MethodPost, strings.TrimRight(*baseURL, "/")+"/auth/register", nil, "")
				if err != nil {
					results <- result{index: index, err: err}
					continue
				}
				if status != http.StatusCreated {
					results <- result{index: index, err: fmt.Errorf("token registration returned HTTP %d: %s", status, body)}
					continue
				}
				var reply struct {
					Token string `json:"token"`
				}
				if err := json.Unmarshal(body, &reply); err != nil || reply.Token == "" {
					results <- result{index: index, err: fmt.Errorf("invalid token registration response: %s", body)}
					continue
				}
				results <- result{index: index, token: reply.Token}
			}
		}()
	}
	go func() {
		for index := 0; index < count; index++ {
			jobs <- index
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	for result := range results {
		if result.err != nil {
			return nil, result.err
		}
		tokens[result.index] = result.token
	}
	return tokens, nil
}

func createShow(client *http.Client, seats []string) (string, error) {
	requestBody := map[string]any{
		"name":        "burst-" + uuid.NewString(),
		"price_paise": 25000,
		"seats":       seats,
	}
	status, body, err := doJSON(client, http.MethodPost, strings.TrimRight(*baseURL, "/")+"/shows", requestBody, "")
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("create show returned HTTP %d: %s", status, body)
	}
	var reply struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &reply); err != nil || reply.ID == "" {
		return "", fmt.Errorf("invalid create-show response: %s", body)
	}
	return reply.ID, nil
}

func runWave(client *http.Client, showID string, count, workerLimit int, requestFor func(int) (string, string, string)) []outcome {
	results := make(chan outcome, count)
	start := make(chan struct{})
	jobs := make(chan int, count)
	for index := 0; index < count; index++ {
		jobs <- index
	}
	close(jobs)
	if workerLimit > count {
		workerLimit = count
	}
	var wg sync.WaitGroup
	for worker := 0; worker < workerLimit; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for index := range jobs {
				token, seat, key := requestFor(index)
				results <- reserve(client, showID, token, seat, key)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	all := make([]outcome, 0, count)
	for result := range results {
		all = append(all, result)
	}
	return all
}

func reserve(client *http.Client, showID, token, seat, key string) outcome {
	body := map[string]any{"seats": []string{seat}}
	request, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/shows/%s/reserve", strings.TrimRight(*baseURL, "/"), showID),
		mustJSON(body))
	if err != nil {
		return outcome{err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", key)
	response, err := client.Do(request)
	if err != nil {
		return outcome{err: err}
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	return outcome{status: response.StatusCode, body: responseBody, err: err}
}

func mustJSON(value any) io.Reader {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return bytes.NewReader(encoded)
}

func reservationID(result outcome) string {
	if result.status != http.StatusCreated || result.err != nil {
		return ""
	}
	var reply struct {
		ReservationID string `json:"reservation_id"`
	}
	if json.Unmarshal(result.body, &reply) != nil {
		return ""
	}
	return reply.ReservationID
}

func summarize(label string, results []outcome) distribution {
	var totals distribution
	for _, result := range results {
		totals.add(result)
	}
	totals.print(label)
	return totals
}

func getShowState(client *http.Client, showID string) (map[string]int, error) {
	status, body, err := doJSON(client, http.MethodGet,
		fmt.Sprintf("%s/shows/%s", strings.TrimRight(*baseURL, "/"), showID), nil, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get show returned HTTP %d: %s", status, body)
	}
	var reply struct {
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return nil, err
	}
	return reply.Counts, nil
}

func availableSeatsMetric(client *http.Client, showID string) (int, error) {
	status, body, err := doJSON(client, http.MethodGet, strings.TrimRight(*baseURL, "/")+"/metrics", nil, "")
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("metrics returned HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	wanted := fmt.Sprintf("seats_available{show_id=%q}", showID)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, wanted) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, wanted))
		if len(fields) != 1 {
			return 0, fmt.Errorf("invalid seats_available sample: %s", line)
		}
		value, err := strconv.Atoi(fields[0])
		return value, err
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("no seats_available metric for show %s", showID)
}

func main() {
	flag.Parse()
	if *concurrency < 1 || *userCount < 1 || *seatCount < 12 {
		log.Fatal("concurrency and users must be positive; seats must be at least 12")
	}
	workerLimit := *workers
	if workerLimit == 0 {
		workerLimit = *concurrency
		// Windows Go clients can exhaust OS threads with 20k simultaneous
		// network waits; keep the default practical while allowing an override.
		if runtime.GOOS == "windows" && workerLimit > 1024 {
			workerLimit = 1024
		}
	}
	if workerLimit < 1 {
		log.Fatal("workers must be positive, or zero to use the platform default")
	}
	transport := &http.Transport{
		MaxIdleConns:        workerLimit,
		MaxIdleConnsPerHost: workerLimit,
		MaxConnsPerHost:     workerLimit,
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Minute}
	defer transport.CloseIdleConnections()

	seats := make([]string, *seatCount)
	for index := range seats {
		seats[index] = fmt.Sprintf("A%d", index)
	}
	showID, err := createShow(client, seats)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Created show %s with %d seats\n", showID, len(seats))

	tokens, err := registerTokens(client, *userCount+2)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Registered %d users for token-authenticated requests\n", len(tokens))

	fmt.Printf("Firing %d hot-seat requests for A0 (up to %d in flight, %d users)...\n", *concurrency, workerLimit, *userCount)
	started := time.Now()
	hotResults := runWave(client, showID, *concurrency, workerLimit, func(index int) (string, string, string) {
		return tokens[index%*userCount], "A0", uuid.NewString()
	})
	fmt.Printf("Hot-seat storm finished in %s\n", time.Since(started).Round(time.Millisecond))
	hotTotals := summarize("hot-seat", hotResults)

	const idempotencyRetries = 10
	idempotencyToken := tokens[len(tokens)-2]
	idempotencyKey := uuid.NewString()
	replayResults := runWave(client, showID, idempotencyRetries, workerLimit, func(int) (string, string, string) {
		return idempotencyToken, "A1", idempotencyKey
	})
	replayTotals := summarize("same-key same-body retries", replayResults)
	replayIDs := make(map[string]struct{})
	for _, result := range replayResults {
		if id := reservationID(result); id != "" {
			replayIDs[id] = struct{}{}
		}
	}

	mismatchResult := reserve(client, showID, idempotencyToken, "A2", idempotencyKey)
	mismatchTotals := summarize("same-key different-body", []outcome{mismatchResult})

	limitToken := tokens[len(tokens)-1]
	limitResults := runWave(client, showID, 10, workerLimit, func(index int) (string, string, string) {
		seat := seats[2+(index%(len(seats)-2))]
		return limitToken, seat, uuid.NewString()
	})
	limitTotals := summarize("one user, 10 parallel seats", limitResults)

	counts, err := getShowState(client, showID)
	if err != nil {
		log.Fatal(err)
	}
	available := counts["available"]
	held := counts["held"]
	confirmed := counts["confirmed"]
	observedTotal := available + held + confirmed
	fmt.Printf("Final API state: available=%d held=%d confirmed=%d total=%d expected=%d\n",
		available, held, confirmed, observedTotal, len(seats))

	metricAvailable, err := availableSeatsMetric(client, showID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Prometheus seats_available=%d (API available=%d)\n", metricAvailable, available)

	allTotals := []distribution{hotTotals, replayTotals, mismatchTotals, limitTotals}
	fiveXX, transportErrors := 0, 0
	for _, totals := range allTotals {
		fiveXX += totals.fiveXX
		transportErrors += totals.transportErrors
	}
	checks := map[string]bool{
		"hot-seat has exactly one winning hold":   hotTotals.held201 == 1 && hotTotals.seatTaken == *concurrency-1,
		"same-key retries return one reservation": replayTotals.held201 == idempotencyRetries && len(replayIDs) == 1,
		"same-key different body is rejected":     mismatchTotals.idempotencyRejected == 1,
		"per-user limit is four seats":            limitTotals.held201 == 4 && limitTotals.perUserLimit == 6,
		"no 5xx or transport errors":              fiveXX == 0 && transportErrors == 0,
		"seat reconciliation holds":               observedTotal == len(seats),
		"database metric matches API state":       metricAvailable == available,
	}
	failed := false
	for _, label := range sortedKeys(checks) {
		fmt.Printf("%s: %s\n", map[bool]string{true: "PASS", false: "FAIL"}[checks[label]], label)
		if !checks[label] {
			failed = true
		}
	}
	if failed {
		log.Fatal("burst verification failed")
	}
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
