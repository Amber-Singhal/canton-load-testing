package workloads

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/digital-asset/canton-load-testing/runner/client"
	"github.com/digital-asset/canton-load-testing/runner/config"
	"github.com/google/uuid"
)

const (
	multiPartyTemplateModule = "MultiPartyTrade"
	tradeProposalTemplate    = "TradeProposal"
	tradeAgreementTemplate   = "TradeAgreement"
)

// MultiParty simulates a three-step trade workflow involving a proposer, a counterparty, and a settlement agent.
// 1. Proposer creates a TradeProposal.
// 2. Counterparty accepts it, creating a TradeAgreement.
// 3. SettlementAgent settles the agreement, archiving it.
type MultiParty struct {
	client  *client.CantonClient
	logger  *log.Logger
	config  *config.WorkloadConfig
	parties []string
}

// NewMultiParty creates a new instance of the MultiParty workload.
func NewMultiParty(c *client.CantonClient, cfg *config.WorkloadConfig, logger *log.Logger) (Workload, error) {
	if cfg.PartyCount < 3 {
		return nil, fmt.Errorf("multiParty workload requires at least 3 parties, but got %d", cfg.PartyCount)
	}
	return &MultiParty{
		client:  c,
		logger:  logger,
		config:  cfg,
		parties: make([]string, 0),
	}, nil
}

func (w *MultiParty) Name() string {
	return "MultiParty"
}

func (w *MultiParty) Run(ctx context.Context, duration time.Duration) (map[string]interface{}, error) {
	w.logger.Printf("Starting MultiParty workload for %v", duration)

	w.logger.Println("Allocating parties...")
	allocatedParties, err := w.client.AllocateParties(ctx, w.config.PartyCount)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate parties: %w", err)
	}
	w.parties = allocatedParties
	w.logger.Printf("Allocated %d parties", len(w.parties))

	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	var wg sync.WaitGroup
	var successCount, errorCount atomic.Int64
	ticker := time.NewTicker(time.Second / time.Duration(w.config.Tps))
	defer ticker.Stop()

	for i := 0; i < w.config.ConcurrentFlows; i++ {
		wg.Add(1)
		go func(workerId int) {
			defer wg.Done()
			w.logger.Printf("Starting worker %d", workerId)
			for {
				select {
				case <-ctx.Done():
					w.logger.Printf("Worker %d stopping", workerId)
					return
				case <-ticker.C:
					err := w.runTransactionCycle(ctx)
					if err != nil {
						errorCount.Add(1)
						w.logger.Printf("Worker %d encountered an error: %v", workerId, err)
					} else {
						successCount.Add(1)
					}
				}
			}
		}(i)
	}

	wg.Wait()
	w.logger.Println("All workers have stopped.")

	totalSuccess := successCount.Load()
	totalError := errorCount.Load()
	// Each successful cycle consists of 3 transactions (create, exercise, exercise)
	totalTransactions := totalSuccess*3 + totalError

	results := map[string]interface{}{
		"workload":               w.Name(),
		"duration":               duration.Seconds(),
		"successful_cycles":      totalSuccess,
		"failed_cycles":          totalError,
		"total_transactions_est": totalTransactions,
		"configured_tps":         w.config.Tps,
		"configured_flows":       w.config.ConcurrentFlows,
	}

	return results, nil
}

// runTransactionCycle executes one full proposal-accept-settle flow.
func (w *MultiParty) runTransactionCycle(ctx context.Context) error {
	// 1. Select parties for the roles
	proposer, counterparty, settlementAgent := w.selectParties()
	tradeId := uuid.New().String()

	// 2. Proposer creates the TradeProposal
	proposalCid, err := w.createTradeProposal(ctx, proposer, counterparty, settlementAgent, tradeId)
	if err != nil {
		return fmt.Errorf("step 1 (propose) failed for trade %s: %w", tradeId, err)
	}

	// 3. Counterparty accepts the proposal
	agreementCid, err := w.exerciseAccept(ctx, counterparty, proposalCid)
	if err != nil {
		return fmt.Errorf("step 2 (accept) failed for trade %s: %w", tradeId, err)
	}

	// 4. SettlementAgent settles the trade
	err = w.exerciseSettle(ctx, settlementAgent, agreementCid)
	if err != nil {
		return fmt.Errorf("step 3 (settle) failed for trade %s: %w", tradeId, err)
	}

	return nil
}

func (w *MultiParty) createTradeProposal(ctx context.Context, proposer, counterparty, settlementAgent, tradeId string) (string, error) {
	templateId := fmt.Sprintf("%s:%s", multiPartyTemplateModule, tradeProposalTemplate)
	payload := map[string]interface{}{
		"templateId": templateId,
		"payload": map[string]interface{}{
			"proposer":        proposer,
			"counterparty":    counterparty,
			"settlementAgent": settlementAgent,
			"tradeId":         tradeId,
			"asset": map[string]interface{}{
				"id":       fmt.Sprintf("ISIN-%s", tradeId[0:8]),
				"quantity": "100.0",
			},
			"price": map[string]interface{}{
				"currency": "USD",
				"amount":   "10000.0",
			},
		},
	}
	return w.client.CreateContract(ctx, proposer, payload)
}

func (w *MultiParty) exerciseAccept(ctx context.Context, counterparty, proposalCid string) (string, error) {
	templateId := fmt.Sprintf("%s:%s", multiPartyTemplateModule, tradeProposalTemplate)
	payload := map[string]interface{}{
		"templateId": templateId,
		"contractId": proposalCid,
		"choice":     "Accept",
		"argument":   map[string]interface{}{},
	}
	return w.client.ExerciseChoice(ctx, counterparty, payload)
}

func (w *MultiParty) exerciseSettle(ctx context.Context, settlementAgent, agreementCid string) error {
	templateId := fmt.Sprintf("%s:%s", multiPartyTemplateModule, tradeAgreementTemplate)
	payload := map[string]interface{}{
		"templateId": templateId,
		"contractId": agreementCid,
		"choice":     "Settle",
		"argument": map[string]interface{}{
			"settlementTime": time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	_, err := w.client.ExerciseChoice(ctx, settlementAgent, payload)
	return err
}

func (w *MultiParty) selectParties() (proposer, counterparty, settlementAgent string) {
	// Ensure three distinct parties are chosen
	p := rand.Perm(len(w.parties))
	proposer = w.parties[p[0]]
	counterparty = w.parties[p[1]]
	settlementAgent = w.parties[p[2]]
	return
}