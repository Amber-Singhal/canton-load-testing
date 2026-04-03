package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// --- Configuration ---

// Config holds all the configuration parameters for the load test runner.
type Config struct {
	LedgerURL     string
	AuthToken     string
	SenderParty   string
	ReceiverParty string
	Concurrency   int
	TotalTx       int
	RateLimit     int // transactions per second
	Timeout       time.Duration
	Workload      string
	Verbose       bool
}

// --- Statistics ---

// Stats holds the collected metrics from the load test run.
type Stats struct {
	mu         sync.Mutex
	totalSent  int64
	success    int64
	failures   int64
	latencies  []time.Duration
}

// AddSuccess records a successful transaction and its latency.
func (s *Stats) AddSuccess(latency time.Duration) {
	atomic.AddInt64(&s.success, 1)
	s.mu.Lock()
	s.latencies = append(s.latencies, latency)
	s.mu.Unlock()
}

// AddFailure records a failed transaction.
func (s *Stats) AddFailure() {
	atomic.AddInt64(&s.failures, 1)
}

// AddSent records that a transaction has been dispatched.
func (s *Stats) AddSent() {
	atomic.AddInt64(&s.totalSent, 1)
}

// --- Runner ---

// Runner orchestrates the load test.
type Runner struct {
	config    Config
	stats     Stats
	client    *http.Client
	startTime time.Time
	endTime   time.Time
}

// NewRunner creates a new load test Runner with the given configuration.
func NewRunner(config Config) *Runner {
	return &Runner{
		config: config,
		stats:  Stats{latencies: make([]time.Duration, 0, config.TotalTx)},
		client: &http.Client{
			Timeout: config.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        config.Concurrency * 2,
				MaxIdleConnsPerHost: config.Concurrency * 2,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// Run executes the load test.
func (r *Runner) Run() {
	log.Printf("Starting load test with config: %+v", r.config)
	r.startTime = time.Now()

	var wg sync.WaitGroup
	jobs := make(chan int, r.config.TotalTx)

	// Start workers
	for i := 1; i <= r.config.Concurrency; i++ {
		wg.Add(1)
		go r.worker(i, jobs, &wg)
	}

	log.Printf("Started %d concurrent workers.", r.config.Concurrency)

	// Distribute work
	var ticker *time.Ticker
	if r.config.RateLimit > 0 {
		interval := time.Second / time.Duration(r.config.RateLimit)
		ticker = time.NewTicker(interval)
		defer ticker.Stop()
	}

	for i := 1; i <= r.config.TotalTx; i++ {
		if ticker != nil {
			<-ticker.C
		}
		r.stats.AddSent()
		jobs <- i
	}
	close(jobs)

	// Wait for all workers to finish
	wg.Wait()
	r.endTime = time.Now()
	log.Println("Load test finished.")
}

// worker is a goroutine that processes transaction jobs.
func (r *Runner) worker(id int, jobs <-chan int, wg *sync.WaitGroup) {
	defer wg.Done()
	for txID := range jobs {
		r.executeTransaction(id, txID)
	}
}

// executeTransaction runs a single transaction based on the configured workload.
func (r *Runner) executeTransaction(workerID, txID int) {
	startTime := time.Now()

	var err error
	switch r.config.Workload {
	case "create-ping":
		err = r.createPingContract(workerID, txID)
	default:
		err = fmt.Errorf("unknown workload: %s", r.config.Workload)
	}

	latency := time.Since(startTime)

	if err != nil {
		r.stats.AddFailure()
		if r.config.Verbose {
			log.Printf("[Worker %d, Tx %d] Error: %v (latency: %s)", workerID, txID, err, latency)
		}
	} else {
		r.stats.AddSuccess(latency)
		if r.config.Verbose {
			log.Printf("[Worker %d, Tx %d] Success (latency: %s)", workerID, txID, latency)
		}
	}
}

// --- JSON API Interaction ---

// CreateRequest defines the payload for the Canton JSON API /v1/create endpoint.
type CreateRequest struct {
	TemplateID string         `json:"templateId"`
	Payload    map[string]any `json:"payload"`
}

// createPingContract sends a request to create a `PingPong.Ping` contract.
func (r *Runner) createPingContract(workerID, txID int) error {
	// Assumes a Daml module named PingPong with template Ping
	// module PingPong where
	//   template Ping
	//     with
	//       sender : Party
	//       receiver : Party
	//       id : Text
	//     where signatory sender; observer receiver
	
	reqPayload := CreateRequest{
		TemplateID: "PingPong:Ping",
		Payload: map[string]any{
			"sender":   r.config.SenderParty,
			"receiver": r.config.ReceiverParty,
			"id":       fmt.Sprintf("tx-%d-%d-%d", workerID, txID, time.Now().UnixNano()),
		},
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", r.config.LedgerURL+"/v1/create", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+r.config.AuthToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ledger API returned non-OK status: %s, body: %s", resp.Status, string(respBody))
	}

	return nil
}

// --- Reporting ---

// Report calculates and prints the final summary of the load test.
func (r *Runner) Report() {
	totalDuration := r.endTime.Sub(r.startTime)
	if totalDuration == 0 {
		log.Println("Total duration was zero, cannot calculate TPS.")
		return
	}

	// Basic stats
	totalSuccess := atomic.LoadInt64(&r.stats.success)
	totalFailures := atomic.LoadInt64(&r.stats.failures)
	totalCompleted := totalSuccess + totalFailures
	actualTPS := float64(totalSuccess) / totalDuration.Seconds()
	successRate := float64(totalSuccess) / float64(totalCompleted) * 100

	fmt.Println("\n--- Load Test Report ---")
	fmt.Printf("Total Duration:      %v\n", totalDuration.Round(time.Millisecond))
	fmt.Printf("Concurrency Level:   %d\n", r.config.Concurrency)
	fmt.Printf("Target Transactions: %d\n", r.config.TotalTx)
	fmt.Printf("Rate Limit (TPS):    %d\n", r.config.RateLimit)
	fmt.Println("---")
	fmt.Printf("Completed Tx:        %d\n", totalCompleted)
	fmt.Printf("Successful Tx:       %d\n", totalSuccess)
	fmt.Printf("Failed Tx:           %d\n", totalFailures)
	fmt.Printf("Success Rate:        %.2f%%\n", successRate)
	fmt.Printf("Achieved TPS:        %.2f\n", actualTPS)
	fmt.Println("---")

	// Latency stats
	if totalSuccess > 0 {
		r.stats.mu.Lock()
		latenciesMs := make([]float64, len(r.stats.latencies))
		var sumLatency time.Duration
		for i, l := range r.stats.latencies {
			latenciesMs[i] = float64(l.Microseconds()) / 1000.0
			sumLatency += l
		}
		r.stats.mu.Unlock()

		sort.Float64s(latenciesMs)

		mean := (sumLatency / time.Duration(totalSuccess)).Round(time.Microsecond)
		p50 := percentile(latenciesMs, 50)
		p90 := percentile(latenciesMs, 90)
		p99 := percentile(latenciesMs, 99)
		min := latenciesMs[0]
		max := latenciesMs[len(latenciesMs)-1]

		fmt.Println("Latency (ms):")
		fmt.Printf("  Mean:              %.3f\n", float64(mean.Microseconds())/1000.0)
		fmt.Printf("  Min:               %.3f\n", min)
		fmt.Printf("  Max:               %.3f\n", max)
		fmt.Printf("  P50 (Median):      %.3f\n", p50)
		fmt.Printf("  P90:               %.3f\n", p90)
		fmt.Printf("  P99:               %.3f\n", p99)
	}
	fmt.Println("------------------------")
}

func percentile(data []float64, perc float64) float64 {
	if len(data) == 0 {
		return 0.0
	}
	index := (perc / 100.0) * float64(len(data)-1)
	if index == float64(int(index)) {
		return data[int(index)]
	}
	lower := int(index)
	upper := lower + 1
	if upper >= len(data) {
		return data[lower]
	}
	fraction := index - float64(lower)
	return data[lower] + fraction*(data[upper]-data[lower])
}

// --- Main Function ---

func main() {
	// Command-line flags
	url := flag.String("url", "http://localhost:7575", "Canton Ledger JSON API URL")
	tokenFile := flag.String("token-file", "", "Path to a file containing the JWT auth token")
	sender := flag.String("sender", "operator", "Daml party to send transactions from")
	receiver := flag.String("receiver", "operator", "Daml party to receive transactions")
	concurrency := flag.Int("c", 10, "Number of concurrent workers")
	totalTx := flag.Int("n", 1000, "Total number of transactions to send")
	rate := flag.Int("r", 0, "Rate limit in transactions per second (0 for no limit)")
	timeout := flag.Duration("t", 30*time.Second, "Timeout for each ledger request")
	workload := flag.String("w", "create-ping", "Workload to run (e.g., 'create-ping')")
	verbose := flag.Bool("v", false, "Enable verbose logging for each transaction")

	flag.Parse()

	if *tokenFile == "" {
		log.Fatal("Error: --token-file flag is required.")
	}
	tokenBytes, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Fatalf("Error reading token file: %v", err)
	}
	token := string(tokenBytes)

	config := Config{
		LedgerURL:     *url,
		AuthToken:     token,
		SenderParty:   *sender,
		ReceiverParty: *receiver,
		Concurrency:   *concurrency,
		TotalTx:       *totalTx,
		RateLimit:     *rate,
		Timeout:       *timeout,
		Workload:      *workload,
		Verbose:       *verbose,
	}

	runner := NewRunner(config)
	runner.Run()
	runner.Report()
}