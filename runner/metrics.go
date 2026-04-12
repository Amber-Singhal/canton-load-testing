package runner

import (
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// MetricsCollector holds the metrics for a load test run.
// It is safe for concurrent use.
type MetricsCollector struct {
	mu              sync.Mutex
	startTime       time.Time
	endTime         time.Time
	requestsSent    int64
	successRequests int64
	errorRequests   int64
	latencies       []time.Duration
}

// NewMetricsCollector creates and initializes a new MetricsCollector.
func NewMetricsCollector() *MetricsCollector {
	// Pre-allocate a reasonable capacity to reduce re-allocations.
	return &MetricsCollector{
		latencies: make([]time.Duration, 0, 10000),
	}
}

// Start marks the beginning of the measurement period.
func (m *MetricsCollector) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startTime = time.Now()
}

// Stop marks the end of the measurement period.
func (m *MetricsCollector) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endTime = time.Now()
}

// RecordSend increments the counter for attempted requests.
func (m *MetricsCollector) RecordSend() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requestsSent++
}

// RecordResult records the outcome of a single request.
func (m *MetricsCollector) RecordResult(latency time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err != nil {
		m.errorRequests++
	} else {
		m.successRequests++
		m.latencies = append(m.latencies, latency)
	}
}

// Summary contains the calculated statistics from a MetricsCollector.
type Summary struct {
	Duration        time.Duration
	TotalRequests   int64
	SuccessRequests int64
	ErrorRequests   int64
	ErrorRate       float64
	Throughput      float64 // Successful requests per second (TPS)
	LatencyMin      time.Duration
	LatencyMax      time.Duration
	LatencyAvg      time.Duration
	LatencyP50      time.Duration // Median
	LatencyP90      time.Duration
	LatencyP95      time.Duration
	LatencyP99      time.Duration
}

// PrintReport calculates, formats, and prints the final metrics report to standard output.
func (m *MetricsCollector) PrintReport() {
	m.mu.Lock()
	if m.startTime.IsZero() {
		m.mu.Unlock()
		log.Println("Metrics collection was not started. Cannot generate report.")
		return
	}
	if m.endTime.IsZero() {
		m.endTime = time.Now()
	}

	// Create copies of the necessary data under the lock. This minimizes lock
	// contention, as the expensive calculations happen on the copies after the lock is released.
	startTime := m.startTime
	endTime := m.endTime
	requestsSent := m.requestsSent
	successRequests := m.successRequests
	errorRequests := m.errorRequests
	latenciesCopy := make([]time.Duration, len(m.latencies))
	copy(latenciesCopy, m.latencies)
	m.mu.Unlock()

	summary := calculateSummary(
		startTime,
		endTime,
		requestsSent,
		successRequests,
		errorRequests,
		latenciesCopy,
	)

	fmt.Println("\n--- Load Test Summary ---")
	fmt.Printf("Duration:\t\t%.2fs\n", summary.Duration.Seconds())
	fmt.Println("-------------------------")
	fmt.Printf("Total Requests:\t\t%d\n", summary.TotalRequests)
	fmt.Printf("Successful Requests:\t%d\n", summary.SuccessRequests)
	fmt.Printf("Failed Requests:\t%d\n", summary.ErrorRequests)
	fmt.Printf("Error Rate:\t\t%.2f%%\n", summary.ErrorRate*100)
	fmt.Println("-------------------------")
	fmt.Printf("Throughput (TPS):\t%.2f\n", summary.Throughput)
	fmt.Println("-------------------------")
	fmt.Println("Latency (successful requests):")
	fmt.Printf("  Min:\t\t\t%s\n", summary.LatencyMin)
	fmt.Printf("  Max:\t\t\t%s\n", summary.LatencyMax)
	fmt.Printf("  Avg:\t\t\t%s\n", summary.LatencyAvg)
	fmt.Printf("  p50 (Median):\t\t%s\n", summary.LatencyP50)
	fmt.Printf("  p90:\t\t\t%s\n", summary.LatencyP90)
	fmt.Printf("  p95:\t\t\t%s\n", summary.LatencyP95)
	fmt.Printf("  p99:\t\t\t%s\n", summary.LatencyP99)
	fmt.Println("-------------------------")
}

// calculateSummary performs the statistical calculations. It is a pure function
// that operates on copies of data, making it safe to run concurrently with the collector.
func calculateSummary(startTime, endTime time.Time, requestsSent, successRequests, errorRequests int64, latencies []time.Duration) Summary {
	duration := endTime.Sub(startTime)
	if duration <= 0 {
		duration = 1 * time.Microsecond // Avoid division by zero with a small non-zero value
	}

	totalCompleted := successRequests + errorRequests
	errorRate := 0.0
	if totalCompleted > 0 {
		errorRate = float64(errorRequests) / float64(totalCompleted)
	}

	throughput := float64(successRequests) / duration.Seconds()

	s := Summary{
		Duration:        duration,
		TotalRequests:   requestsSent,
		SuccessRequests: successRequests,
		ErrorRequests:   errorRequests,
		ErrorRate:       errorRate,
		Throughput:      throughput,
	}

	if len(latencies) == 0 {
		return s
	}

	// Sort latencies to calculate percentiles
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	s.LatencyMin = latencies[0]
	s.LatencyMax = latencies[len(latencies)-1]
	s.LatencyP50 = percentile(latencies, 50.0)
	s.LatencyP90 = percentile(latencies, 90.0)
	s.LatencyP95 = percentile(latencies, 95.0)
	s.LatencyP99 = percentile(latencies, 99.0)

	var totalLatency time.Duration
	for _, l := range latencies {
		totalLatency += l
	}
	s.LatencyAvg = totalLatency / time.Duration(len(latencies))

	return s
}

// percentile calculates the p-th percentile of a latencies slice.
// Assumes the slice is already sorted and non-empty.
func percentile(latencies []time.Duration, p float64) time.Duration {
	// Calculate index using the nearest-rank method on a 0-indexed slice.
	index := int((p / 100.0) * float64(len(latencies)-1))
	return latencies[index]
}