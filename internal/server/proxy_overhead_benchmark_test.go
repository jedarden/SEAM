package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkProxyBaseline measures baseline proxy overhead without any middleware
func BenchmarkProxyBaseline(b *testing.B) {
	// Create mock upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	// Create proxy
	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkProxyWithSmallPayload measures proxy overhead with small JSON payload
func BenchmarkProxyWithSmallPayload(b *testing.B) {
	requestBody := []byte(`{"test":"data"}`)
	responseBody := []byte(`{"result":"success","data":"test"}`)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read request body
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/api/test", bytes.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkProxyWithMediumPayload measures proxy overhead with medium payload (10KB)
func BenchmarkProxyWithMediumPayload(b *testing.B) {
	requestBody := bytes.Repeat([]byte("x"), 10*1024)  // 10 KB
	responseBody := bytes.Repeat([]byte("y"), 50*1024) // 50 KB

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/api/test", bytes.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/octet-stream")
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkProxyWithLargePayload measures proxy overhead with large payload (100KB)
func BenchmarkProxyWithLargePayload(b *testing.B) {
	requestBody := bytes.Repeat([]byte("x"), 100*1024)  // 100 KB
	responseBody := bytes.Repeat([]byte("y"), 500*1024) // 500 KB

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/api/test", bytes.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/octet-stream")
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkProxyWithHeaders measures proxy overhead with various headers
func BenchmarkProxyWithHeaders(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Header", "custom-value")
		w.Header().Set("X-Request-Id", "req-123")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.Header.Set("Authorization", "Bearer token123")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "TestAgent/1.0")
		req.Header.Set("X-Custom-Header", "custom-value")
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkProxyParallel measures proxy performance under concurrent load
func BenchmarkProxyParallel(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("GET", "/api/test", nil)
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				b.Fatalf("expected status 200, got %d", w.Code)
			}
		}
	})
}

// BenchmarkProxyMemoryFootprint measures memory usage per concurrent connection
func BenchmarkProxyMemoryFootprint(b *testing.B) {
	concurrentLevels := []int{1, 10, 50, 100, 500}

	for _, concurrency := range concurrentLevels {
		b.Run(fmt.Sprintf("Concurrent-%d", concurrency), func(b *testing.B) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"status":"ok"}`))
			}))
			defer upstream.Close()

			proxy, err := NewReverseProxy(upstream.URL)
			if err != nil {
				b.Fatalf("Failed to create proxy: %v", err)
			}

			// Force GC before benchmark
			runtime.GC()
			var m1 runtime.MemStats
			runtime.ReadMemStats(&m1)

			b.ResetTimer()

			done := make(chan struct{})
			requestsPerGoroutine := b.N / concurrency

			// Launch concurrent goroutines
			for i := 0; i < concurrency; i++ {
				go func() {
					for j := 0; j < requestsPerGoroutine; j++ {
						req := httptest.NewRequest("GET", "/api/test", nil)
						w := httptest.NewRecorder()
						proxy.ServeHTTP(w, req)
					}
					done <- struct{}{}
				}()
			}

			// Wait for all goroutines to complete
			for i := 0; i < concurrency; i++ {
				<-done
			}

			b.StopTimer()

			// Force GC and measure memory
			runtime.GC()
			var m2 runtime.MemStats
			runtime.ReadMemStats(&m2)

			// Report memory used per operation
			memUsed := m2.TotalAlloc - m1.TotalAlloc
			ops := int64(concurrency * requestsPerGoroutine)
			b.ReportMetric(float64(memUsed)/float64(ops), "B/req")
			b.ReportMetric(float64(m2.Alloc)/1024/1024, "MiB_total")
		})
	}
}

// BenchmarkProxyThroughputSaturation measures throughput at different saturation points
func BenchmarkProxyThroughputSaturation(b *testing.B) {
	targetRPS := []int{100, 500, 1000, 5000, 10000}

	for _, rps := range targetRPS {
		b.Run(fmt.Sprintf("RPS-%d", rps), func(b *testing.B) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"status":"ok"}`))
			}))
			defer upstream.Close()

			proxy, err := NewReverseProxy(upstream.URL)
			if err != nil {
				b.Fatalf("Failed to create proxy: %v", err)
			}

			interval := time.Second / time.Duration(rps)

			b.ResetTimer()

			startTime := time.Now()
			successCount := int64(0)

			for i := 0; i < b.N; i++ {
				// Throttle to target RPS
				elapsed := time.Since(startTime)
				expectedDuration := time.Duration(i) * interval
				if elapsed < expectedDuration {
					time.Sleep(expectedDuration - elapsed)
				}

				req := httptest.NewRequest("GET", "/api/test", nil)
				w := httptest.NewRecorder()
				proxy.ServeHTTP(w, req)

				if w.Code == http.StatusOK {
					atomic.AddInt64(&successCount, 1)
				}
			}

			actualRPS := float64(atomic.LoadInt64(&successCount)) / time.Since(startTime).Seconds()
			b.ReportMetric(actualRPS, "req/sec")
		})
	}
}

// BenchmarkProxyLatencyDistribution measures latency percentiles under load
func BenchmarkProxyLatencyDistribution(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	b.ResetTimer()

	latencies := make([]time.Duration, b.N)
	for i := 0; i < b.N; i++ {
		start := time.Now()
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)
		latencies[i] = time.Since(start)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}

	b.StopTimer()

	// Calculate percentiles
	p50, p95, p99 := proxyPercentiles(latencies)

	b.ReportMetric(float64(p50.Microseconds()), "µs_p50")
	b.ReportMetric(float64(p95.Microseconds()), "µs_p95")
	b.ReportMetric(float64(p99.Microseconds()), "µs_p99")
}

// BenchmarkProxyByMethod measures proxy overhead by HTTP method
func BenchmarkProxyByMethod(b *testing.B) {
	methods := []string{"GET", "POST", "PUT", "PATCH", "DELETE"}

	for _, method := range methods {
		b.Run(method, func(b *testing.B) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"result":"ok"}`))
			}))
			defer upstream.Close()

			proxy, err := NewReverseProxy(upstream.URL)
			if err != nil {
				b.Fatalf("Failed to create proxy: %v", err)
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				var body io.Reader
				if method == "POST" || method == "PUT" || method == "PATCH" {
					body = bytes.NewReader([]byte(`{"test":"data"}`))
				}

				req := httptest.NewRequest(method, "/api/test", body)
				if body != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				w := httptest.NewRecorder()
				proxy.ServeHTTP(w, req)

				if w.Code != http.StatusOK {
					b.Fatalf("expected status 200, got %d", w.Code)
				}
			}
		})
	}
}

// BenchmarkProxyWithQueryParams measures proxy overhead with various query parameters
func BenchmarkProxyWithQueryParams(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		url := fmt.Sprintf("/api/test?param1=value1&param2=value2&page=%d&limit=10&sort=desc", i%100)
		req := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkProxyWithUpstreamDelay measures proxy overhead with slow upstream
func BenchmarkProxyWithUpstreamDelay(b *testing.B) {
	delays := []time.Duration{0, 10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond}

	for _, delay := range delays {
		b.Run(fmt.Sprintf("Delay-%s", delay), func(b *testing.B) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(delay)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"status":"ok"}`))
			}))
			defer upstream.Close()

			proxy, err := NewReverseProxy(upstream.URL)
			if err != nil {
				b.Fatalf("Failed to create proxy: %v", err)
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				req := httptest.NewRequest("GET", "/api/test", nil)
				w := httptest.NewRecorder()
				proxy.ServeHTTP(w, req)

				if w.Code != http.StatusOK {
					b.Fatalf("expected status 200, got %d", w.Code)
				}
			}
		})
	}
}

// BenchmarkProxyConnectionReuse measures benefit of HTTP connection reuse
func BenchmarkProxyConnectionReuse(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkProxyForwardRequest measures ForwardRequest function performance
func BenchmarkProxyForwardRequest(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer upstream.Close()

	parsedURL, _ := url.Parse(upstream.URL)
	// Extract host:port from test server URL
	upstreamBase := fmt.Sprintf("http://%s", parsedURL.Host)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)

		ctx := context.Background()
		resp, err := ForwardRequest(ctx, req, nil, upstreamBase)
		if err != nil {
			b.Fatalf("ForwardRequest failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			b.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
	}
}

// BenchmarkProxyScalingBehavior measures how performance scales with concurrent load
func BenchmarkProxyScalingBehavior(b *testing.B) {
	concurrencyLevels := []int{1, 2, 4, 8, 16, 32}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("Goroutines-%d", concurrency), func(b *testing.B) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"status":"ok"}`))
			}))
			defer upstream.Close()

			proxy, err := NewReverseProxy(upstream.URL)
			if err != nil {
				b.Fatalf("Failed to create proxy: %v", err)
			}

			b.ResetTimer()
			b.ReportAllocs()

			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					req := httptest.NewRequest("GET", "/api/test", nil)
					w := httptest.NewRecorder()
					proxy.ServeHTTP(w, req)

					if w.Code != http.StatusOK {
						b.Fatalf("expected status 200, got %d", w.Code)
					}
				}
			})
		})
	}
}

// BenchmarkProxyWithChunkedResponse measures proxy overhead with chunked transfer encoding
func BenchmarkProxyWithChunkedResponse(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(http.StatusOK)

		// Write response in chunks
		for i := 0; i < 10; i++ {
			fmt.Fprintf(w, `{"chunk":"%d"}`, i)
			w.(http.Flusher).Flush()
		}
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("Failed to create proxy: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// proxyPercentiles calculates p50, p95, and p99 percentiles from a slice of durations
func proxyPercentiles(durations []time.Duration) (p50, p95, p99 time.Duration) {
	if len(durations) == 0 {
		return 0, 0, 0
	}

	// Make a copy to avoid modifying the original slice
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)

	// Simple bubble sort (good enough for benchmarking)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] > sorted[j] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	p50 = sorted[len(sorted)*50/100]
	p95 = sorted[len(sorted)*95/100]
	p99 = sorted[len(sorted)*99/100]

	return
}
