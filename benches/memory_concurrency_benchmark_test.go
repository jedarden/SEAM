package benches

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/server"
)

// MemoryStats captures memory metrics at a point in time
type MemoryStats struct {
	Timestamp         time.Time
	Alloc             uint64
	TotalAlloc        uint64
	Sys               uint64
	HeapAlloc         uint64
	HeapSys           uint64
	HeapIdle          uint64
	HeapInuse         uint64
	HeapReleased      uint64
	HeapObjects       uint64
	StackInuse        uint64
	StackSys          uint64
	MSpanInuse        uint64
	MSpanSys          uint64
	MCacheInuse       uint64
	MCacheSys         uint64
	BuckHashSys       uint64
	GCSys             uint64
	NextGC            uint64
	LastGC            uint64
	PauseTotalNs      uint64
	NumGC             uint32
	NumForcedGC       uint32
	GCCPUFraction     float64
	Goroutines        int
	Connections       int
	RequestsPerSecond float64
}

// ConcurrencyResult captures results for a concurrency test
type ConcurrencyResult struct {
	ConcurrentConnections int
	TotalRequests         int64
	SuccessfulRequests    int64
	FailedRequests        int64
	Duration              time.Duration
	RequestsPerSecond     float64
	MemoryBefore          MemoryStats
	MemoryAfter           MemoryStats
	MemoryPerConnection   uint64
	AvgLatency            time.Duration
	P50Latency            time.Duration
	P95Latency            time.Duration
	P99Latency            time.Duration
	GCPauses              uint32
}

// ConnectionPool manages multiple concurrent connections
type ConnectionPool struct {
	clients   []*http.Client
	serverURL string
	mu        sync.RWMutex
	active    int32
}

// NewConnectionPool creates a new connection pool
func NewConnectionPool(serverURL string, maxSize int) *ConnectionPool {
	pool := &ConnectionPool{
		serverURL: serverURL,
		clients:   make([]*http.Client, 0, maxSize),
	}
	return pool
}

// GetClient returns an HTTP client from the pool or creates a new one
func (p *ConnectionPool) GetClient() *http.Client {
	p.mu.RLock()
	if len(p.clients) > 0 {
		client := p.clients[len(p.clients)-1]
		p.clients = p.clients[:len(p.clients)-1]
		p.mu.RUnlock()
		return client
	}
	p.mu.RUnlock()

	// Create new client with connection pooling disabled for accurate measurement
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        0,
			MaxIdleConnsPerHost: 0,
			DisableKeepAlives:   false,
		},
	}
}

// ReturnClient returns a client to the pool
func (p *ConnectionPool) ReturnClient(client *http.Client) {
	p.mu.Lock()
	p.clients = append(p.clients, client)
	p.mu.Unlock()
}

// getMemoryStats captures current memory statistics
func getMemoryStats(connections int, rps float64) MemoryStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return MemoryStats{
		Timestamp:         time.Now(),
		Alloc:             m.Alloc,
		TotalAlloc:        m.TotalAlloc,
		Sys:               m.Sys,
		HeapAlloc:         m.HeapAlloc,
		HeapSys:           m.HeapSys,
		HeapIdle:          m.HeapIdle,
		HeapInuse:         m.HeapInuse,
		HeapReleased:      m.HeapReleased,
		HeapObjects:       m.HeapObjects,
		StackInuse:        m.StackInuse,
		StackSys:          m.StackSys,
		MSpanInuse:        m.MSpanInuse,
		MSpanSys:          m.MSpanSys,
		MCacheInuse:       m.MCacheInuse,
		MCacheSys:         m.MCacheSys,
		BuckHashSys:       m.BuckHashSys,
		GCSys:             m.GCSys,
		NextGC:            m.NextGC,
		LastGC:            m.LastGC,
		PauseTotalNs:      m.PauseTotalNs,
		NumGC:             m.NumGC,
		NumForcedGC:       m.NumForcedGC,
		GCCPUFraction:     m.GCCPUFraction,
		Goroutines:        runtime.NumGoroutine(),
		Connections:       connections,
		RequestsPerSecond: rps,
	}
}

// forceGC forces a garbage collection cycle
func forceGC() {
	runtime.GC()
	// Give GC time to complete
	runtime.Gosched()
}

// BenchmarkMemoryPerConnection measures memory footprint per connection at rest
func BenchmarkMemoryPerConnection(b *testing.B) {
	// Create upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	// Create proxy
	proxy, err := server.NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	connectionLevels := []int{1, 10, 50, 100, 500, 1000}

	for _, conns := range connectionLevels {
		b.Run(fmt.Sprintf("Connections-%d", conns), func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()

			// Force GC before measurement
			forceGC()

			for i := 0; i < b.N; i++ {
				// Create connections
				clients := make([]*http.Client, conns)
				for j := 0; j < conns; j++ {
					clients[j] = &http.Client{
						Timeout: 5 * time.Second,
					}
				}

				// Measure memory before requests
				memBefore := getMemoryStats(conns, 0)

				// Make one request per connection
				var wg sync.WaitGroup
				for j := 0; j < conns; j++ {
					wg.Add(1)
					go func(client *http.Client) {
						defer wg.Done()
						req, _ := http.NewRequest("GET", proxyServer.URL, nil)
						resp, err := client.Do(req)
						if err == nil {
							io.Copy(io.Discard, resp.Body)
							resp.Body.Close()
						}
					}(clients[j])
				}
				wg.Wait()

				// Measure memory after requests
				memAfter := getMemoryStats(conns, 0)

				// Calculate per-connection memory
				memPerConn := uint64(0)
				if conns > 0 && memAfter.HeapAlloc > memBefore.HeapAlloc {
					memPerConn = (memAfter.HeapAlloc - memBefore.HeapAlloc) / uint64(conns)
				}

				b.ReportMetric(float64(memPerConn), "bytes/conn")
				b.ReportMetric(float64(memAfter.HeapAlloc)/1024, "KB/heap")
				b.ReportMetric(float64(runtime.NumGoroutine()), "goroutines")
			}
		})
	}
}

// BenchmarkConcurrentThroughput measures throughput under concurrent load
func BenchmarkConcurrentThroughput(b *testing.B) {
	// Create upstream server
	requestCount := atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	// Create proxy
	proxy, err := server.NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	concurrencyLevels := []int{1, 10, 50, 100, 500, 1000}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("Concurrent-%d", concurrency), func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()

			results := make([]ConcurrencyResult, 0, b.N)

			for i := 0; i < b.N; i++ {
				requestCount.Store(0)

				// Force GC before test
				forceGC()
				memBefore := getMemoryStats(concurrency, 0)

				// Start concurrent workers
				startTime := time.Now()
				successful := atomic.Int64{}
				failed := atomic.Int64{}
				latencies := make(chan time.Duration, concurrency*10)

				var wg sync.WaitGroup
				for j := 0; j < concurrency; j++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						client := &http.Client{
							Timeout: 10 * time.Second,
						}

						// Make multiple requests per connection
						for k := 0; k < 10; k++ {
							reqStart := time.Now()
							req, _ := http.NewRequest("GET", proxyServer.URL, nil)
							resp, err := client.Do(req)
							latency := time.Since(reqStart)

							if err != nil {
								failed.Add(1)
								continue
							}

							io.Copy(io.Discard, resp.Body)
							resp.Body.Close()
							successful.Add(1)
							latencies <- latency
						}
					}()
				}

				wg.Wait()
				close(latencies)
				duration := time.Since(startTime)

				// Measure memory after test
				memAfter := getMemoryStats(concurrency, float64(successful.Load())/duration.Seconds())

				// Calculate percentiles
				latencySlice := make([]time.Duration, 0, len(latencies))
				for lat := range latencies {
					latencySlice = append(latencySlice, lat)
				}

				p50, p95, p99 := percentiles(latencySlice)

				result := ConcurrencyResult{
					ConcurrentConnections: concurrency,
					TotalRequests:         int64(concurrency * 10),
					SuccessfulRequests:    successful.Load(),
					FailedRequests:        failed.Load(),
					Duration:              duration,
					RequestsPerSecond:     float64(successful.Load()) / duration.Seconds(),
					MemoryBefore:          memBefore,
					MemoryAfter:           memAfter,
					MemoryPerConnection:   0,
					AvgLatency:            duration / time.Duration(successful.Load()),
					P50Latency:            p50,
					P95Latency:            p95,
					P99Latency:            p99,
					GCPauses:              memAfter.NumGC - memBefore.NumGC,
				}

				if concurrency > 0 {
					result.MemoryPerConnection = (memAfter.HeapAlloc - memBefore.HeapAlloc) / uint64(concurrency)
				}

				results = append(results, result)

				b.ReportMetric(result.RequestsPerSecond, "req/s")
				b.ReportMetric(float64(result.MemoryPerConnection), "bytes/conn")
				b.ReportMetric(float64(result.P50Latency.Nanoseconds())/1e6, "ms/p50")
				b.ReportMetric(float64(result.P95Latency.Nanoseconds())/1e6, "ms/p95")
				b.ReportMetric(float64(result.P99Latency.Nanoseconds())/1e6, "ms/p99")
				b.ReportMetric(float64(memAfter.HeapAlloc)/1024, "KB/heap")
				b.ReportMetric(float64(result.GCPauses), "gc_cycles")
			}
		})
	}
}

// BenchmarkMemoryGrowth measures memory growth patterns over time
func BenchmarkMemoryGrowth(b *testing.B) {
	// Create upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	// Create proxy
	proxy, err := server.NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	concurrencyLevels := []int{10, 50, 100}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("Concurrent-%d", concurrency), func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				// Force GC and establish baseline
				forceGC()
				baselineMem := getMemoryStats(0, 0)

				// Track memory over time
				samples := make([]MemoryStats, 0, 11)
				samples = append(samples, baselineMem)

				client := &http.Client{
					Timeout: 10 * time.Second,
				}

				// Run for 10 seconds, sampling every second
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				ticker := time.NewTicker(1 * time.Second)
				defer ticker.Stop()

				complete := atomic.Bool{}
				go func() {
					for !complete.Load() {
						req, _ := http.NewRequest("GET", proxyServer.URL, nil)
						resp, err := client.Do(req)
						if err == nil {
							io.Copy(io.Discard, resp.Body)
							resp.Body.Close()
						}
					}
				}()

				for j := 0; j < 10; j++ {
					select {
					case <-ticker.C:
						mem := getMemoryStats(concurrency, 0)
						samples = append(samples, mem)
					case <-ctx.Done():
						break
					}
				}

				complete.Store(true)
				cancel()

				// Calculate growth metrics
				if len(samples) >= 2 {
					initialMem := samples[0].HeapAlloc
					finalMem := samples[len(samples)-1].HeapAlloc
					memGrowth := int64(finalMem) - int64(initialMem)
					growthRate := float64(memGrowth) / float64(finalMem) * 100

					b.ReportMetric(growthRate, "growth_percent")
					b.ReportMetric(float64(finalMem)/1024, "KB/final_heap")

					// Check if growth is linear or sub-linear
					// We expect sub-linear growth due to connection reuse and GC
					if growthRate < 10 {
						b.ReportMetric(1, "sub_linear")
					} else if growthRate < 50 {
						b.ReportMetric(0, "linear")
					} else {
						b.ReportMetric(-1, "super_linear")
					}
				}
			}
		})
	}
}

// BenchmarkGCImpact measures GC impact under concurrent load
func BenchmarkGCImpact(b *testing.B) {
	// Create upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	// Create proxy
	proxy, err := server.NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	concurrencyLevels := []int{10, 50, 100, 500, 1000}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("Concurrent-%d", concurrency), func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				// Reset GC stats
				runtime.ReadMemStats(&runtime.MemStats{})
				initialGC := atomic.Uint32{}
				initialGC.Store(runtime.MemStats{}.NumGC)

				// Run concurrent work
				var wg sync.WaitGroup
				client := &http.Client{
					Timeout: 10 * time.Second,
				}

				for j := 0; j < concurrency; j++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						// Make enough requests to trigger GC
						for k := 0; k < 100; k++ {
							req, _ := http.NewRequest("GET", proxyServer.URL, nil)
							resp, err := client.Do(req)
							if err == nil {
								io.Copy(io.Discard, resp.Body)
								resp.Body.Close()
							}
						}
					}()
				}

				wg.Wait()

				// Read final memory stats
				var finalMem runtime.MemStats
				runtime.ReadMemStats(&finalMem)

				gcCycles := finalMem.NumGC - initialGC.Load()
				avgPauseNs := uint64(0)
				if gcCycles > 0 {
					avgPauseNs = finalMem.PauseTotalNs / uint64(gcCycles)
				}

				b.ReportMetric(float64(gcCycles), "gc_cycles")
				b.ReportMetric(float64(avgPauseNs)/1e6, "ms/gc_avg")
				b.ReportMetric(float64(finalMem.GCCPUFraction)*100, "cpu_gc_percent")
				b.ReportMetric(float64(finalMem.HeapAlloc)/1024, "KB/heap")
				b.ReportMetric(float64(finalMem.NextGC)/1024, "KB/next_gc")
			}
		})
	}
}

// BenchmarkConnectionScaling tests performance scaling across different connection counts
func BenchmarkConnectionScaling(b *testing.B) {
	// Create upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	// Create proxy
	proxy, err := server.NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	scales := []struct {
		name        string
		connections []int
	}{
		{"Low", []int{1, 5, 10}},
		{"Medium", []int{25, 50, 100}},
		{"High", []int{250, 500, 1000}},
	}

	for _, scale := range scales {
		b.Run(scale.name, func(b *testing.B) {
			for _, conns := range scale.connections {
				b.Run(fmt.Sprintf("Connections-%d", conns), func(b *testing.B) {
					b.ResetTimer()
					b.ReportAllocs()

					for i := 0; i < b.N; i++ {
						forceGC()

						var wg sync.WaitGroup
						requestCount := atomic.Int64{}
						successCount := atomic.Int64{}
						startTime := time.Now()

						for j := 0; j < conns; j++ {
							wg.Add(1)
							go func() {
								defer wg.Done()
								client := &http.Client{
									Timeout: 10 * time.Second,
								}

								// Each connection makes 5 requests
								for k := 0; k < 5; k++ {
									requestCount.Add(1)
									req, _ := http.NewRequest("GET", proxyServer.URL, nil)
									resp, err := client.Do(req)
									if err == nil {
										io.Copy(io.Discard, resp.Body)
										resp.Body.Close()
										successCount.Add(1)
									}
								}
							}()
						}

						wg.Wait()
						duration := time.Since(startTime)

						throughput := float64(successCount.Load()) / duration.Seconds()
						successRate := float64(successCount.Load()) / float64(requestCount.Load()) * 100

						b.ReportMetric(throughput, "req/s")
						b.ReportMetric(successRate, "success_rate")
					}
				})
			}
		})
	}
}

// percentiles calculates p50, p95, p99 percentiles from a slice of durations
func percentiles(durations []time.Duration) (p50, p95, p99 time.Duration) {
	if len(durations) == 0 {
		return 0, 0, 0
	}

	// Simple sort (not efficient for large datasets but sufficient for benchmarks)
	for i := 0; i < len(durations); i++ {
		for j := i + 1; j < len(durations); j++ {
			if durations[i] > durations[j] {
				durations[i], durations[j] = durations[j], durations[i]
			}
		}
	}

	p50Idx := len(durations) * 50 / 100
	p95Idx := len(durations) * 95 / 100
	p99Idx := len(durations) * 99 / 100

	if p50Idx >= len(durations) {
		p50Idx = len(durations) - 1
	}
	if p95Idx >= len(durations) {
		p95Idx = len(durations) - 1
	}
	if p99Idx >= len(durations) {
		p99Idx = len(durations) - 1
	}

	return durations[p50Idx], durations[p95Idx], durations[p99Idx]
}

// BenchmarkMemoryAtRest measures memory footprint with idle connections
func BenchmarkMemoryAtRest(b *testing.B) {
	// Create upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	// Create proxy
	proxy, err := server.NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	connectionCounts := []int{0, 1, 10, 50, 100, 500, 1000}

	for _, conns := range connectionCounts {
		b.Run(fmt.Sprintf("IdleConnections-%d", conns), func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				forceGC()
				memBefore := getMemoryStats(0, 0)

				// Create idle connections (HTTP clients with keep-alive)
				clients := make([]*http.Client, conns)
				for j := 0; j < conns; j++ {
					req, _ := http.NewRequest("GET", proxyServer.URL, nil)
					client := &http.Client{
						Timeout: 30 * time.Second,
					}
					resp, err := client.Do(req)
					if err == nil {
						io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
						clients[j] = client
					}
				}

				// Wait for connections to establish
				time.Sleep(100 * time.Millisecond)

				// Measure memory with idle connections
				memAfter := getMemoryStats(conns, 0)

				// Calculate memory per idle connection
				memPerConn := uint64(0)
				if conns > 0 && memAfter.Sys > memBefore.Sys {
					memPerConn = (memAfter.Sys - memBefore.Sys) / uint64(conns)
				}

				b.ReportMetric(float64(memPerConn), "bytes/idle_conn")
				b.ReportMetric(float64(memAfter.Sys)/1024, "KB/sys")
				b.ReportMetric(float64(memAfter.HeapAlloc)/1024, "KB/heap")
				b.ReportMetric(float64(runtime.NumGoroutine()), "goroutines")
			}
		})
	}
}

// TestMemoryConcurrencyInfrastructure verifies the benchmark infrastructure works
func TestMemoryConcurrencyInfrastructure(t *testing.T) {
	// Create upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	// Create proxy
	proxy, err := server.NewReverseProxy(upstream.URL)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// Test basic request
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest("GET", proxyServer.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Test memory stats capture
	memStats := getMemoryStats(1, 100)
	if memStats.Goroutines < 1 {
		t.Error("Expected at least 1 goroutine")
	}

	t.Logf("Memory stats captured: Alloc=%d, Sys=%d, HeapAlloc=%d",
		memStats.Alloc, memStats.Sys, memStats.HeapAlloc)
	t.Log("Memory/concurrency infrastructure test passed")
}
