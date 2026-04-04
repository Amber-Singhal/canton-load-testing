package workloads

import (
	"bytes"
	"canton-load-testing/runner/auth"
	"canton-load-testing/runner/config"
	"canton-load-testing/runner/reports"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// SimpleTransferWorkload implements a workload that creates a set of tokens
// and proposals in a setup phase, and then drives a constant TPS of transferring
// those tokens by accepting the proposals.
type SimpleTransferWorkload struct {
	Cfg              *config.SimpleTransferConfig
	Stats            *reports.Stats
	Client           *http.Client
	proposalCidsChan chan string // Channel to hold contract IDs of proposals created in Setup
}

// --- JSON API Data Structures ---

type createPayload struct {
	TemplateID string      `json:"templateId"`
	Payload    interface{} `json:"payload"`
}

type exercisePayload struct {
	TemplateID string      `json:"templateId"`
	ContractID string      `json:"contractId"`
	Choice     string      `json:"choice"`
	Argument   interface{} `json:"argument"`
}

type queryPayload struct {
	TemplateIDs []string `json:"templateIds"`
}

// --- Daml Contract-Specific Payloads ---

type simpleToken struct {
	Owner  string `json:"owner"`
	Issuer string `json:"issuer"`
	ID     string `json:"id"`
}

type transferProposal struct {
	TokenCid string `json:"tokenCid"`
	NewOwner string `json:"newOwner"`
}

// NewSimpleTransferWorkload creates a new instance of the simple transfer workload.
func NewSimpleTransferWorkload(cfg *config.SimpleTransferConfig, stats *reports.Stats) *SimpleTransferWorkload {
	// A shared HTTP client is efficient for connection reuse.
	// Ensure these values are tuned for high concurrency.
	transport := &http.Transport{
		MaxIdleConns:        cfg.Concurrency * 2,
		MaxIdleConnsPerHost: cfg.Concurrency * 2,
		IdleConnTimeout:     90 * time.Second,
	}
	return &SimpleTransferWorkload{
		Cfg:   cfg,
		Stats: stats,
		Client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		proposalCidsChan: make(chan string, cfg.NumContracts),
	}
}

// Name returns the identifier for this workload.
func (w *SimpleTransferWorkload) Name() string {
	return "SimpleTransfer"
}

// Setup creates the initial set of contracts required for the Run phase.
// It creates N tokens owned by Alice, and for each token, a proposal to transfer it to Bob.
// This pre-computation ensures the Run phase only measures the target transaction ('Accept').
func (w *SimpleTransferWorkload) Setup(ctx context.Context) error {
	log.Printf("[SimpleTransfer] Starting setup...")
	startTime := time.Now()

	aliceToken, err := auth.GetParticipantToken(w.Cfg.Alice.ParticipantID)
	if err != nil {
		return fmt.Errorf("failed to get alice token: %w", err)
	}

	// Clean slate: archive any contracts from previous runs to ensure a clean test environment.
	// This requires the Daml models to have archive/cancel choices.
	templatesToClean := []string{w.Cfg.ProposalTemplateID, w.Cfg.TokenTemplateID}
	if err := w.archiveExistingContracts(ctx, &w.Cfg.Alice, aliceToken, templatesToClean); err != nil {
		log.Printf("WARN: Failed to archive existing contracts for Alice, continuing anyway: %v", err)
	}

	// Concurrently create tokens and proposals using a worker pool pattern.
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(w.Cfg.Concurrency)

	log.Printf("[SimpleTransfer] Creating %d tokens and transfer proposals...", w.Cfg.NumContracts)
	for i := 0; i < w.Cfg.NumContracts; i++ {
		iteration := i
		g.Go(func() error {
			// Step 1: Alice creates a SimpleToken contract.
			tokenID := fmt.Sprintf("token-%d-%d", startTime.Unix(), iteration)
			createToken := createPayload{
				TemplateID: w.Cfg.TokenTemplateID,
				Payload: simpleToken{
					Owner:  w.Cfg.Alice.Party,
					Issuer: w.Cfg.Alice.Party,
					ID:     tokenID,
				},
			}
			tokenCid, err := w.createContract(gCtx, &w.Cfg.Alice, aliceToken, createToken)
			if err != nil {
				return fmt.Errorf("failed to create token %d: %w", iteration, err)
			}

			// Step 2: Alice creates a TransferProposal for the new token.
			createProposal := createPayload{
				TemplateID: w.Cfg.ProposalTemplateID,
				Payload: transferProposal{
					TokenCid: tokenCid,
					NewOwner: w.Cfg.Bob.Party,
				},
			}
			proposalCid, err := w.createContract(gCtx, &w.Cfg.Alice, aliceToken, createProposal)
			if err != nil {
				return fmt.Errorf("failed to create proposal for token %s: %w", tokenCid, err)
			}

			w.proposalCidsChan <- proposalCid
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("error during concurrent contract creation: %w", err)
	}

	close(w.proposalCidsChan)
	log.Printf("[SimpleTransfer] Setup completed in %v. Created %d proposals.", time.Since(startTime), w.Cfg.NumContracts)
	return nil
}

// Run executes the main load test, driving transactions at the target TPS.
func (w *SimpleTransferWorkload) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	log.Printf("[SimpleTransfer] Starting run: %d TPS for %ds...", w.Cfg.Tps, w.Cfg.DurationSeconds)

	bobToken, err := auth.GetParticipantToken(w.Cfg.Bob.ParticipantID)
	if err != nil {
		log.Printf("ERROR: [SimpleTransfer] Failed to get bob token: %v", err)
		return
	}

	runDuration := time.Duration(w.Cfg.DurationSeconds) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, runDuration)
	defer cancel()

	limiter := rate.NewLimiter(rate.Limit(w.Cfg.Tps), w.Cfg.Tps)
	workChan := make(chan string, w.Cfg.Concurrency)
	var workersWg sync.WaitGroup

	// Start worker pool
	for i := 0; i < w.Cfg.Concurrency; i++ {
		workersWg.Add(1)
		go w.runWorker(runCtx, &workersWg, bobToken, workChan)
	}

	// Dispatcher loop: feeds work to workers at the specified rate.
	for proposalCid := range w.proposalCidsChan {
		if err := limiter.Wait(runCtx); err != nil {
			break // Context cancelled (e.g., timeout).
		}

		select {
		case workChan <- proposalCid:
			// Dispatched work successfully.
		case <-runCtx.Done():
			break // Time's up, stop dispatching.
		}
	}

	close(workChan)      // Signal workers that no more work is coming.
	workersWg.Wait()     // Wait for all workers to finish.
	log.Printf("[SimpleTransfer] Run finished.", w.Name())
}

// runWorker is a single worker that pulls proposal CIDs from a channel and exercises the Accept choice.
func (w *SimpleTransferWorkload) runWorker(ctx context.Context, wg *sync.WaitGroup, bobToken string, workChan <-chan string) {
	defer wg.Done()
	for proposalCid := range workChan {
		start := time.Now()
		ex := exercisePayload{
			TemplateID: w.Cfg.ProposalTemplateID,
			ContractID: proposalCid,
			Choice:     "Accept",
			Argument:   map[string]interface{}{},
		}

		_, err := w.exerciseChoice(ctx, &w.Cfg.Bob, bobToken, ex)
		latency := time.Since(start)

		w.Stats.Record(reports.Result{
			Workload:  w.Name(),
			Success:   err == nil,
			Latency:   latency,
			Timestamp: time.Now(),
			Error:     err,
		})
	}
}

// --- HTTP/JSON API Helper Functions ---

func (w *SimpleTransferWorkload) apiRequest(ctx context.Context, method, url, token string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	return w.Client.Do(req)
}

func (w *SimpleTransferWorkload) createContract(ctx context.Context, target *config.ParticipantConfig, token string, payload createPayload) (string, error) {
	url := fmt.Sprintf("http://%s:%d/v1/create", target.Host, target.Port)
	jsonBody, _ := json.Marshal(payload)

	resp, err := w.apiRequest(ctx, "POST", url, token, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode create response: %w", err)
	}

	contractID, ok := result["contractId"].(string)
	if !ok {
		return "", fmt.Errorf("contractId not found or not a string in response")
	}
	return contractID, nil
}

func (w *SimpleTransferWorkload) exerciseChoice(ctx context.Context, target *config.ParticipantConfig, token string, payload exercisePayload) (interface{}, error) {
	url := fmt.Sprintf("http://%s:%d/v1/exercise", target.Host, target.Port)
	jsonBody, _ := json.Marshal(payload)

	resp, err := w.apiRequest(ctx, "POST", url, token, bytes.NewBuffer(jsonBody))
	if err != nil {
		atomic.AddUint64(&w.Stats.NetworkErrors, 1)
		return nil, fmt.Errorf("exercise request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		atomic.AddUint64(&w.Stats.LedgerErrors, 1)
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("exercise failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode exercise response: %w", err)
	}

	return result["result"], nil
}

func (w *SimpleTransferWorkload) archiveExistingContracts(ctx context.Context, target *config.ParticipantConfig, token string, templateIDs []string) error {
	log.Printf("[SimpleTransfer] Archiving existing contracts for party %s: %v", target.Party, templateIDs)
	url := fmt.Sprintf("http://%s:%d/v1/query", target.Host, target.Port)
	query := queryPayload{TemplateIDs: templateIDs}
	jsonBody, _ := json.Marshal(query)

	resp, err := w.apiRequest(ctx, "POST", url, token, bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("query request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("query failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var queryResult struct {
		Result []struct {
			ContractID string `json:"contractId"`
			TemplateID string `json:"templateId"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&queryResult); err != nil {
		return fmt.Errorf("failed to decode query response: %w", err)
	}

	if len(queryResult.Result) == 0 {
		log.Printf("[SimpleTransfer] No existing contracts to archive for %s.", target.Party)
		return nil
	}

	log.Printf("[SimpleTransfer] Found %d contracts to archive for %s. Archiving...", len(queryResult.Result), target.Party)
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(50) // Limit concurrent archives

	for _, contract := range queryResult.Result {
		c := contract // capture loop variable
		g.Go(func() error {
			var choiceName string
			// This logic depends on the Daml model having specific choices for cleanup.
			if c.TemplateID == w.Cfg.ProposalTemplateID {
				choiceName = "Cancel"
			} else if c.TemplateID == w.Cfg.TokenTemplateID {
				choiceName = "Discard"
			} else {
				return nil // Don't know how to archive this template
			}

			ex := exercisePayload{
				TemplateID: c.TemplateID,
				ContractID: c.ContractID,
				Choice:     choiceName,
				Argument:   map[string]interface{}{},
			}
			_, err := w.exerciseChoice(gCtx, target, token, ex)
			return err
		})
	}
	return g.Wait()
}