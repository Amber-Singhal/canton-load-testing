package workloads

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/canton-net/canton-load-testing/runner/ledger"
	"github.com/canton-net/canton-load-testing/runner/metrics"
)

const (
	// Assuming a Daml model with these template IDs from a DVP library
	cashTemplateID        = "Dvp.Asset:Cash"
	securityTemplateID    = "Dvp.Asset:Security"
	dvpProposalTemplateID = "Dvp.Settlement:DvpProposal"
)

// DvpSettlementWorkloadConfig holds configuration for the DVP settlement workload.
type DvpSettlementWorkloadConfig struct {
	LedgerURL          string
	NumPairs           int // Number of buyer/seller pairs
	Transactions       int // Total transactions to run
	Concurrency        int
	Timeout            time.Duration
	CashIssuerHint     string
	SecurityIssuerHint string
	BuyerHintPrefix    string
	SellerHintPrefix   string
}

// DvpSettlementWorkload simulates a high-throughput Delivery-versus-Payment (DVP) settlement scenario.
// It involves two asset types (Cash, Security) and a proposal/acceptance
// workflow to atomically swap them between pairs of buyers and sellers. The assets
// are swapped back and forth between the pairs to sustain the load.
type DvpSettlementWorkload struct {
	cfg     DvpSettlementWorkloadConfig
	client  *ledger.Client
	metrics *metrics.Collector
}

// dvpParties holds the allocated parties for the workload.
type dvpParties struct {
	CashIssuer     *ledger.PartyInfo
	SecurityIssuer *ledger.PartyInfo
	Buyers         []*ledger.PartyInfo
	Sellers        []*ledger.PartyInfo
}

// NewDvpSettlementWorkload creates a new instance of the DVP settlement workload.
func NewDvpSettlementWorkload(cfg DvpSettlementWorkloadConfig) *DvpSettlementWorkload {
	if cfg.NumPairs <= 0 {
		cfg.NumPairs = 10
	}
	if cfg.Transactions <= 0 {
		cfg.Transactions = 1000
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 10
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &DvpSettlementWorkload{
		cfg:     cfg,
		client:  ledger.NewClient(cfg.LedgerURL, cfg.Timeout),
		metrics: metrics.NewCollector(),
	}
}

// Run executes the workload, measures performance, and prints a summary.
func (w *DvpSettlementWorkload) Run(ctx context.Context) error {
	log.Printf("Starting DVP Settlement workload: %d pairs, %d transactions, %d concurrency", w.cfg.NumPairs, w.cfg.Transactions, w.cfg.Concurrency)
	rand.Seed(time.Now().UnixNano())

	log.Println("Setting up parties...")
	parties, err := w.setupParties(ctx)
	if err != nil {
		return fmt.Errorf("failed to set up parties: %w", err)
	}
	log.Printf("... %d parties allocated.", 2+w.cfg.NumPairs*2)

	log.Println("Issuing initial assets...")
	if err := w.issueInitialAssets(ctx, parties); err != nil {
		return fmt.Errorf("failed to issue initial assets: %w", err)
	}
	log.Println("... initial assets issued.")

	log.Println("Starting settlement loop...")
	w.metrics.Start()
	w.runSettlementLoop(ctx, parties)
	w.metrics.StopAndReport()

	return nil
}

// setupParties allocates all necessary parties for the workload concurrently.
func (w *DvpSettlementWorkload) setupParties(ctx context.Context) (*dvpParties, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	parties := &dvpParties{
		Buyers:  make([]*ledger.PartyInfo, w.cfg.NumPairs),
		Sellers: make([]*ledger.PartyInfo, w.cfg.NumPairs),
	}

	// Allocate Issuers
	wg.Add(2)
	go func() {
		defer wg.Done()
		p, err := w.client.AllocateParty(ctx, w.cfg.CashIssuerHint)
		if err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
			return
		}
		parties.CashIssuer = p
	}()
	go func() {
		defer wg.Done()
		p, err := w.client.AllocateParty(ctx, w.cfg.SecurityIssuerHint)
		if err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
			return
		}
		parties.SecurityIssuer = p
	}()
	wg.Wait()

	if len(errs) > 0 {
		return nil, fmt.Errorf("failed to allocate issuer parties: %w", errs[0])
	}

	// Allocate Buyers and Sellers in parallel
	for i := 0; i < w.cfg.NumPairs; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			hint := fmt.Sprintf("%s-%d", w.cfg.BuyerHintPrefix, idx)
			p, err := w.client.AllocateParty(ctx, hint)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			parties.Buyers[idx] = p
		}(i)
		go func(idx int) {
			defer wg.Done()
			hint := fmt.Sprintf("%s-%d", w.cfg.SellerHintPrefix, idx)
			p, err := w.client.AllocateParty(ctx, hint)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			parties.Sellers[idx] = p
		}(i)
	}

	wg.Wait()
	if len(errs) > 0 {
		return nil, fmt.Errorf("%d errors occurred during party allocation, first error: %w", len(errs), errs[0])
	}
	return parties, nil
}

// issueInitialAssets creates one cash contract for each buyer and one security contract for each seller.
func (w *DvpSettlementWorkload) issueInitialAssets(ctx context.Context, parties *dvpParties) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	issuerCashToken, err := w.client.GetToken(ctx, parties.CashIssuer.Identifier)
	if err != nil {
		return fmt.Errorf("failed to get token for cash issuer: %w", err)
	}
	issuerSecurityToken, err := w.client.GetToken(ctx, parties.SecurityIssuer.Identifier)
	if err != nil {
		return fmt.Errorf("failed to get token for security issuer: %w", err)
	}

	// Issue Cash to Buyers (initial state)
	for _, buyer := range parties.Buyers {
		wg.Add(1)
		go func(b *ledger.PartyInfo) {
			defer wg.Done()
			payload := map[string]interface{}{
				"issuer":   parties.CashIssuer.Identifier,
				"owner":    b.Identifier,
				"amount":   "1000000.0000000000",
				"currency": "USD",
			}
			_, err := w.client.CreateContract(ctx, issuerCashToken, cashTemplateID, payload)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("failed to issue cash to %s: %w", b.DisplayName, err))
				mu.Unlock()
			}
		}(buyer)
	}

	// Issue Securities to Sellers (initial state)
	for i, seller := range parties.Sellers {
		wg.Add(1)
		go func(s *ledger.PartyInfo, idx int) {
			defer wg.Done()
			payload := map[string]interface{}{
				"issuer":   parties.SecurityIssuer.Identifier,
				"owner":    s.Identifier,
				"cusip":    fmt.Sprintf("CUSIP%04d", idx),
				"quantity": "1000.0000000000",
			}
			_, err := w.client.CreateContract(ctx, issuerSecurityToken, securityTemplateID, payload)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("failed to issue security to %s: %w", s.DisplayName, err))
				mu.Unlock()
			}
		}(seller, i)
	}

	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("%d errors occurred during asset issuance, first error: %w", len(errs), errs[0])
	}
	return nil
}

// runSettlementLoop creates a pool of workers to execute DVP transactions concurrently.
func (w *DvpSettlementWorkload) runSettlementLoop(ctx context.Context, parties *dvpParties) {
	var wg sync.WaitGroup
	jobs := make(chan int, w.cfg.Transactions)

	for i := 0; i < w.cfg.Concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for txIndex := range jobs {
				// Each transaction randomly picks a pair and which direction the trade goes.
				// This simulates a more dynamic market and avoids thundering herd issues on a single pair's assets.
				pairIndex := rand.Intn(w.cfg.NumPairs)
				p1 := parties.Buyers[pairIndex]
				p2 := parties.Sellers[pairIndex]

				// Randomly assign buyer/seller roles for this specific transaction
				var buyer, seller *ledger.PartyInfo
				if rand.Intn(2) == 0 {
					buyer, seller = p1, p2
				} else {
					buyer, seller = p2, p1
				}

				w.executeSingleDvp(ctx, buyer, seller, txIndex)
			}
		}(i)
	}

	for i := 0; i < w.cfg.Transactions; i++ {
		jobs <- i
	}
	close(jobs)

	wg.Wait()
}

// executeSingleDvp performs one full DVP cycle: find assets, propose, and accept.
func (w *DvpSettlementWorkload) executeSingleDvp(ctx context.Context, buyer, seller *ledger.PartyInfo, txIndex int) {
	start := time.Now()

	buyerToken, err := w.client.GetToken(ctx, buyer.Identifier)
	if err != nil {
		w.metrics.RecordFailure(fmt.Errorf("dvp-%d: failed to get buyer token for %s: %w", txIndex, buyer.DisplayName, err))
		return
	}
	sellerToken, err := w.client.GetToken(ctx, seller.Identifier)
	if err != nil {
		w.metrics.RecordFailure(fmt.Errorf("dvp-%d: failed to get seller token for %s: %w", txIndex, seller.DisplayName, err))
		return
	}

	// Step 1: Seller finds their Security to sell.
	securityContract, err := w.client.QueryOne(ctx, sellerToken, securityTemplateID, map[string]interface{}{"owner": seller.Identifier})
	if err != nil {
		w.metrics.RecordFailure(fmt.Errorf("dvp-%d: %s failed to query security: %w", txIndex, seller.DisplayName, err))
		return
	}
	if securityContract == nil {
		w.metrics.RecordFailure(fmt.Errorf("dvp-%d: %s has no security to sell", txIndex, seller.DisplayName))
		return
	}

	// Step 2: Seller creates DvpProposal
	proposalPayload := map[string]interface{}{
		"seller":       seller.Identifier,
		"buyer":        buyer.Identifier,
		"securityCid":  securityContract.ID,
		"securityData": securityContract.Payload,
		"price":        "98765.4300000000",
		"currency":     "USD",
	}
	proposal, err := w.client.CreateContract(ctx, sellerToken, dvpProposalTemplateID, proposalPayload)
	if err != nil {
		w.metrics.RecordFailure(fmt.Errorf("dvp-%d: %s failed to create proposal: %w", txIndex, seller.DisplayName, err))
		return
	}

	// Step 3: Buyer finds their Cash to use.
	cashContract, err := w.client.QueryOne(ctx, buyerToken, cashTemplateID, map[string]interface{}{"owner": buyer.Identifier})
	if err != nil {
		w.metrics.RecordFailure(fmt.Errorf("dvp-%d: %s failed to query cash: %w", txIndex, buyer.DisplayName, err))
		return
	}
	if cashContract == nil {
		w.metrics.RecordFailure(fmt.Errorf("dvp-%d: %s has no cash to buy with", txIndex, buyer.DisplayName))
		return
	}

	// Step 4: Buyer accepts the proposal, triggering the atomic settlement
	acceptArg := map[string]interface{}{
		"cashCid": cashContract.ID,
	}
	_, err = w.client.ExerciseChoice(ctx, buyerToken, dvpProposalTemplateID, proposal.ID, "Accept", acceptArg)
	if err != nil {
		w.metrics.RecordFailure(fmt.Errorf("dvp-%d: %s failed to accept proposal %s: %w", txIndex, buyer.DisplayName, proposal.ID, err))
		return
	}

	w.metrics.RecordSuccess(time.Since(start))
}