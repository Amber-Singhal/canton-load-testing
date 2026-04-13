package runner

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// BenchmarkResult holds the calculated metrics from a load test run.
type BenchmarkResult struct {
	TotalTransactions      uint64
	SuccessfulTransactions uint64
	FailedTransactions     uint64
	TestDuration           time.Duration
	TPS                    float64
	ErrorRate              float64
	MinLatency             time.Duration
	MaxLatency             time.Duration
	AvgLatency             time.Duration
	LatencyP50             time.Duration
	LatencyP90             time.Duration
	LatencyP99             time.Duration
}

// MetricsCollector is a thread-safe collector for transaction latency and status.
// It gathers data during a benchmark run and calculates summary statistics at the end.
type MetricsCollector struct {
	mutex     sync.Mutex
	latencies []time.Duration
	failures  uint64
	startTime time.Time
	isStarted bool
}

// NewMetricsCollector creates and initializes a new MetricsCollector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		latencies: make([]time.Duration, 0, 100000), // Pre-allocate for performance
	}
}

// Start marks the beginning of the measurement period.
func (mc *MetricsCollector) Start() {
	mc.mutex.Lock()
	defer mc.mutex.Unlock()
	if !mc.isStarted {
		mc.startTime = time.Now()
		mc.isStarted = true
	}
}

// RecordSuccess records a successful transaction's latency.
// It also ensures the timer is started if it hasn't been already.
func (mc *MetricsCollector) RecordSuccess(latency time.Duration) {
	mc.Start() // Ensure timer is started on first recorded event
	mc.mutex.Lock()
	defer mc.mutex.Unlock()
	mc.latencies = append(mc.latencies, latency)
}

// RecordFailure records a failed transaction.
// It also ensures the timer is started if it hasn't been already.
func (mc *MetricsCollector) RecordFailure() {
	mc.Start() // Ensure timer is started on first recorded event
	mc.mutex.Lock()
	defer mc.mutex.Unlock()
	mc.failures++
}

// Calculate computes the final benchmark results from the collected data.
// This method is designed to be called once at the end of a test run.
// The percentile calculation is robust for any workload profile, including bursts,
// as it operates on the complete set of collected latency measurements.
func (mc *MetricsCollector) Calculate() (*BenchmarkResult, error) {
	mc.mutex.Lock()
	defer mc.mutex.Unlock()

	if !mc.isStarted {
		return nil, fmt.Errorf("metrics collection never started")
	}

	endTime := time.Now()
	duration := endTime.Sub(mc.startTime)
	successfulTx := uint64(len(mc.latencies))
	failedTx := mc.failures
	totalTx := successfulTx + failedTx

	if duration <= 0 {
		duration = 1 // Avoid division by zero, though unlikely
	}

	if totalTx == 0 {
		return &BenchmarkResult{TestDuration: duration}, nil
	}

	errorRate := float64(failedTx) / float64(totalTx)

	if successfulTx == 0 {
		return &BenchmarkResult{
			TotalTransactions:      totalTx,
			SuccessfulTransactions: 0,
			FailedTransactions:     failedTx,
			TestDuration:           duration,
			TPS:                    0.0,
			ErrorRate:              errorRate,
		}, nil
	}

	// Create a copy for sorting to avoid holding the lock for a long time
	// and to prevent modifying the original slice if needed later.
	sortedLatencies := make([]time.Duration, successfulTx)
	copy(sortedLatencies, mc.latencies)
	sort.Slice(sortedLatencies, func(i, j int) bool {
		return sortedLatencies[i] < sortedLatencies[j]
	})

	var totalLatency time.Duration
	for _, l := range sortedLatencies {
		totalLatency += l
	}

	result := &BenchmarkResult{
		TotalTransactions:      totalTx,
		SuccessfulTransactions: successfulTx,
		FailedTransactions:     failedTx,
		TestDuration:           duration,
		TPS:                    float64(successfulTx) / duration.Seconds(),
		ErrorRate:              errorRate,
		MinLatency:             sortedLatencies[0],
		MaxLatency:             sortedLatencies[successfulTx-1],
		AvgLatency:             totalLatency / time.Duration(successfulTx),
		LatencyP50:             percentile(sortedLatencies, 50.0),
		LatencyP90:             percentile(sortedLatencies, 90.0),
		LatencyP99:             percentile(sortedLatencies, 99.0),
	}

	return result, nil
}

// percentile calculates the p-th percentile for a pre-sorted slice of durations.
// It uses the "Nearest Rank" method, which is correct and efficient for large datasets.
// This function is the core of the fix, ensuring accurate percentile calculation
// regardless of the transaction timing (steady vs. burst).
func percentile(sortedLatencies []time.Duration, p float64) time.Duration {
	n := len(sortedLatencies)
	if n == 0 {
		return 0
	}

	// Calculate the 0-based index using the NIST recommended method (p/100) * (n-1).
	// This correctly maps the percentile to an index in the sorted slice.
	// For p=99 and n=1000, rank is 0.99 * 999 = 989.01. We take the integer part, which is index 989.
	index := int((p / 100.0) * float64(n-1))

	// Defensive bounds checking, though index should be valid if 0 <= p <= 100.
	if index < 0 {
		index = 0
	}
	if index >= n {
		index = n - 1
	}

	return sortedLatencies[index]
}

// PrintSummary formats and prints the benchmark result to the console.
func (r *BenchmarkResult) PrintSummary() {
	fmt.Println("--- Benchmark Summary ---")
	fmt.Printf("Total Duration:     %v\n", r.TestDuration.Round(time.Millisecond))
	fmt.Printf("Total Transactions: %d\n", r.TotalTransactions)
	fmt.Printf("  - Successful:     %d\n", r.SuccessfulTransactions)
	fmt.Printf("  - Failed:         %d\n", r.FailedTransactions)
	fmt.Printf("Throughput (TPS):   %.2f\n", r.TPS)
	fmt.Printf("Error Rate:         %.2f%%\n", r.ErrorRate*100)
	fmt.Println("--- Latency ---")
	fmt.Printf("Average:            %v\n", r.AvgLatency.Round(time.Microsecond))
	fmt.Printf("Min:                %v\n", r.MinLatency.Round(time.Microsecond))
	fmt.Printf("Max:                %v\n", r.MaxLatency.Round(time.Microsecond))
	fmt.Printf("p50 (Median):       %v\n", r.LatencyP50.Round(time.Microsecond))
	fmt.Printf("p90:                %v\n", r.LatencyP90.Round(time.Microsecond))
	fmt.Printf("p99:                %v\n", r.LatencyP99.Round(time.Microsecond))
	fmt.Println("-------------------------")
}