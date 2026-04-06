package workloads

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"canton-load-testing/runner/client"
	"canton-load-testing/runner/config"
)

// SequencerSaturationWorkload implements the Workload interface for testing maximum sequencer throughput.
// It achieves this by submitting a high volume of minimal, single-party 'create' transactions
// concurrently. This isolates the sequencer's performance by minimizing participant node overhead.
//
// The corresponding Daml model should be extremely simple, e.g.:
// template Ping
//   with
//     p : Party
//   where
//     signatory p
type SequencerSaturationWorkload struct{}

// Name returns the name of the workload.
func (w *SequencerSaturationWorkload) Name() string {
	return "sequencer-saturation"
}

// Description returns a short description of the workload.
func (w *SequencerSaturationWorkload) Description() string {
	return "Tests maximum sequencer throughput with minimal, single-party 'create' transactions."
}

// Run executes the sequencer saturation workload.
func (w *SequencerSaturationWorkload) Run(cfg *config.Config, clients []*client.LedgerClient) (float64, error) {
	if len(clients) == 0 {
		return 0, fmt.Errorf("at least one ledger client is required for the sequencer-saturation workload")
	}
	if len(cfg.Parties) == 0 {
		return 0, fmt.Errorf("at least one party is required in the configuration")
	}

	workloadCfg := cfg.Workloads.SequencerSaturation
	totalTx := workloadCfg.TotalTransactions
	concurrency := cfg.Concurrency
	party := cfg.Parties[0] // Use the first configured party as the signatory

	log.Printf("Starting Sequencer Saturation workload...")
	log.Printf("  Total Transactions: %d", totalTx)
	log.Printf("  Concurrency Level:  %d", concurrency)
	log.Printf("  Target Party:       %s", party)
	log.Printf("  Daml Template:      %s", workloadCfg.Template)

	// The JSON payload for the /v1/create endpoint.
	// This payload is reused by all concurrent workers to minimize allocations.
	createPayload := map[string]interface{}{
		"templateId": workloadCfg.Template,
		"payload": map[string]interface{}{
			"p": party,
		},
	}

	// Create a buffered channel to act as a job queue.
	jobs := make(chan struct{}, totalTx)
	for i := 0; i < totalTx; i++ {
		jobs <- struct{}{}
	}
	close(jobs)

	var successfulTx, failedTx int64
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Printf("Submitting %d transactions with %d workers...", totalTx, concurrency)
	startTime := time.Now()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			// Each worker gets a client to use, round-robin style.
			// This distributes the load if clients for multiple participant nodes are provided.
			ledgerClient := clients[workerID%len(clients)]

			for range jobs {
				// Create a context with a timeout for each individual request.
				reqCtx, reqCancel := context.WithTimeout(ctx, 30*time.Second)
				err := ledgerClient.Create(reqCtx, createPayload)
				reqCancel() // Release resources associated with the request context.

				if err != nil {
					atomic.AddInt64(&failedTx, 1)
					// Log the first few errors to help with debugging, but avoid flooding the logs.
					if atomic.LoadInt64(&failedTx) < 5 {
						log.Printf("Worker %d: Create command failed: %v", workerID, err)
					}
				} else {
					atomic.AddInt64(&successfulTx, 1)
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	log.Println("Workload finished.")
	log.Printf("  Total Duration:          %v", duration)
	log.Printf("  Successful Transactions: %d", successfulTx)
	log.Printf("  Failed Transactions:     %d", failedTx)

	if duration.Seconds() <= 0 {
		log.Println("Duration was zero or negative, cannot calculate TPS.")
		return 0, nil
	}

	tps := float64(successfulTx) / duration.Seconds()
	log.Printf("  Achieved TPS:            %.2f", tps)

	if failedTx > 0 {
		return tps, fmt.Errorf("%d out of %d transactions failed", failedTx, totalTx)
	}

	return tps, nil
}