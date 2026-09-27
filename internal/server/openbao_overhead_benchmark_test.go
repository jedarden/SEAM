package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// MockOpenBaoClient simulates OpenBao secret retrieval for benchmarking
type MockOpenBaoClient struct {
	latency        time.Duration
	requestCount   int64
	cacheHitCount  int64
	cacheMissCount int64
	mu             sync.Mutex
	secretCache    map[string][]byte
	cacheEnabled   bool
}

// NewMockOpenBaoClient creates a mock OpenBao client with configurable latency
func NewMockOpenBaoClient(latency time.Duration, cacheEnabled bool) *MockOpenBaoClient {
	return &MockOpenBaoClient{
		latency:      latency,
		secretCache:  make(map[string][]byte),
		cacheEnabled: cacheEnabled,
	}
}

// GetSecret retrieves a secret, simulating OpenBao API call latency
func (m *MockOpenBaoClient) GetSecret(ctx context.Context, path string) ([]byte, error) {
	m.mu.Lock()
	m.requestCount++

	// Check cache first if enabled
	if m.cacheEnabled {
		if secret, found := m.secretCache[path]; found {
			m.cacheHitCount++
			m.mu.Unlock()
			return secret, nil
		}
		m.cacheMissCount++
	}
	m.mu.Unlock()

	// Simulate network latency to OpenBao
	select {
	case <-time.After(m.latency):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Simulate secret data
	secret := []byte(fmt.Sprintf(`{"token":"secret-%s","type":"bearer"}`, path))

	// Cache the result if caching is enabled
	if m.cacheEnabled {
		m.mu.Lock()
		m.secretCache[path] = secret
		m.mu.Unlock()
	}

	return secret, nil
}

// GetMetrics returns the current metrics
func (m *MockOpenBaoClient) GetMetrics() (requests, hits, misses int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestCount, m.cacheHitCount, m.cacheMissCount
}

// BenchmarkOpenBaoSecretRetrievalBaseline measures baseline secret retrieval overhead
func BenchmarkOpenBaoSecretRetrievalBaseline(b *testing.B) {
	latencies := []time.Duration{
		0,
		1 * time.Millisecond,
		5 * time.Millisecond,
		10 * time.Millisecond,
		50 * time.Millisecond,
	}

	for _, latency := range latencies {
		b.Run(fmt.Sprintf("Latency-%s", latency), func(b *testing.B) {
			client := NewMockOpenBaoClient(latency, false)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				ctx := context.Background()
				path := fmt.Sprintf("seam/routes/test%d", i%100)
				_, err := client.GetSecret(ctx, path)
				if err != nil {
					b.Fatalf("GetSecret failed: %v", err)
				}
			}
		})
	}
}

// BenchmarkOpenBaoWithCaching measures secret retrieval with caching enabled
func BenchmarkOpenBaoWithCaching(b *testing.B) {
	latencies := []time.Duration{
		1 * time.Millisecond,
		5 * time.Millisecond,
		10 * time.Millisecond,
	}

	for _, latency := range latencies {
		b.Run(fmt.Sprintf("Latency-%s", latency), func(b *testing.B) {
			client := NewMockOpenBaoClient(latency, true)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				ctx := context.Background()
				// Use limited set of paths to benefit from caching
				path := fmt.Sprintf("seam/routes/test%d", i%10)
				_, err := client.GetSecret(ctx, path)
				if err != nil {
					b.Fatalf("GetSecret failed: %v", err)
				}
			}

			requests, hits, _ := client.GetMetrics()
			hitRate := float64(hits) / float64(requests) * 100
			b.ReportMetric(hitRate, "hit_rate%%")
		})
	}
}

// BenchmarkOpenBaoConcurrentRetrieval measures secret retrieval under concurrent load
func BenchmarkOpenBaoConcurrentRetrieval(b *testing.B) {
	concurrencyLevels := []int{1, 10, 50, 100}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("Concurrent-%d", concurrency), func(b *testing.B) {
			client := NewMockOpenBaoClient(5*time.Millisecond, true)

			b.ResetTimer()
			b.ReportAllocs()

			b.RunParallel(func(pb *testing.PB) {
				routeNum := 0
				for pb.Next() {
					ctx := context.Background()
					path := fmt.Sprintf("seam/routes/test%d", routeNum%100)
					routeNum++
					_, err := client.GetSecret(ctx, path)
					if err != nil {
						b.Fatalf("GetSecret failed: %v", err)
					}
				}
			})

			requests, hits, _ := client.GetMetrics()
			hitRate := float64(hits) / float64(requests) * 100
			b.ReportMetric(hitRate, "hit_rate%%")
		})
	}
}

// BenchmarkOpenBaoProxyIntegration measures end-to-end proxy with OpenBao secret injection
func BenchmarkOpenBaoProxyIntegration(b *testing.B) {
	latencies := []time.Duration{
		0,
		5 * time.Millisecond,
		10 * time.Millisecond,
	}

	for _, latency := range latencies {
		b.Run(fmt.Sprintf("OpenBaoLatency-%s", latency), func(b *testing.B) {
			openBaoClient := NewMockOpenBaoClient(latency, true)

			// Create upstream server that expects injected secret
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify secret was injected
				authHeader := r.Header.Get("Authorization")
				if authHeader == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
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
				// Simulate secret retrieval before proxying
				ctx := context.Background()
				path := fmt.Sprintf("seam/routes/service%d", i%10)
				secret, err := openBaoClient.GetSecret(ctx, path)
				if err != nil {
					b.Fatalf("GetSecret failed: %v", err)
				}

				// Create request with secret injection
				req := httptest.NewRequest("GET", "/api/test", nil)
				req.Header.Set("Authorization", string(secret))
				w := httptest.NewRecorder()
				proxy.ServeHTTP(w, req)

				if w.Code != http.StatusOK {
					b.Fatalf("expected status 200, got %d", w.Code)
				}
			}

			requests, hits, _ := openBaoClient.GetMetrics()
			hitRate := float64(hits) / float64(requests) * 100
			b.ReportMetric(hitRate, "hit_rate%%")
		})
	}
}

// BenchmarkOpenBaoMemoryFootprint measures memory usage per cached secret
func BenchmarkOpenBaoMemoryFootprint(b *testing.B) {
	secretSizes := []int{
		100,   // Small secret
		1024,  // 1KB secret
		10240, // 10KB secret
	}

	for _, size := range secretSizes {
		b.Run(fmt.Sprintf("SecretSize-%dB", size), func(b *testing.B) {
			client := NewMockOpenBaoClient(0, true)

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				ctx := context.Background()
				path := fmt.Sprintf("seam/routes/secret%d", i%1000)

				// Create mock secret of specified size
				secret := make([]byte, size)
				copy(secret, []byte(fmt.Sprintf(`{"token":"%s"}`, path)))

				// Simulate caching behavior
				client.mu.Lock()
				client.secretCache[path] = secret
				client.mu.Unlock()

				// Retrieve from cache
				_, err := client.GetSecret(ctx, path)
				if err != nil {
					b.Fatalf("GetSecret failed: %v", err)
				}
			}

			b.StopTimer()

			// Report cache size
			client.mu.Lock()
			cacheSize := len(client.secretCache)
			client.mu.Unlock()
			b.ReportMetric(float64(cacheSize), "cached_secrets")
		})
	}
}

// BenchmarkOpenBaoCacheEfficiency measures cache hit rates with different access patterns
func BenchmarkOpenBaoCacheEfficiency(b *testing.B) {
	patterns := []struct {
		name   string
		unique int
		total  int
	}{
		{"Sequential", 100, 100},
		{"Repeated-10", 10, 100},
		{"Repeated-50", 50, 100},
		{"Repeated-90", 90, 100},
	}

	for _, pattern := range patterns {
		b.Run(pattern.name, func(b *testing.B) {
			client := NewMockOpenBaoClient(5*time.Millisecond, true)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				ctx := context.Background()
				path := fmt.Sprintf("seam/routes/test%d", (i % pattern.unique))
				_, err := client.GetSecret(ctx, path)
				if err != nil {
					b.Fatalf("GetSecret failed: %v", err)
				}
			}

			requests, hits, _ := client.GetMetrics()
			hitRate := float64(hits) / float64(requests) * 100
			expectedHitRate := float64(pattern.total-pattern.unique) / float64(pattern.total) * 100

			b.ReportMetric(hitRate, "hit_rate%%")
			b.ReportMetric(expectedHitRate, "expected_hit%%")
		})
	}
}

// BenchmarkOpenBaoTimeoutBehavior measures performance under timeout conditions
func BenchmarkOpenBaoTimeoutBehavior(b *testing.B) {
	timeouts := []time.Duration{
		1 * time.Millisecond,
		10 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond,
	}

	for _, timeout := range timeouts {
		b.Run(fmt.Sprintf("Timeout-%s", timeout), func(b *testing.B) {
			// Create client with latency higher than timeout
			client := NewMockOpenBaoClient(timeout*2, false)

			b.ResetTimer()
			b.ReportAllocs()

			successCount := int64(0)
			timeoutCount := int64(0)

			for i := 0; i < b.N; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				path := fmt.Sprintf("seam/routes/test%d", i%100)

				_, err := client.GetSecret(ctx, path)
				cancel()

				if err == nil {
					successCount++
				} else {
					timeoutCount++
				}
			}

			successRate := float64(successCount) / float64(b.N) * 100
			b.ReportMetric(successRate, "success_rate%%")
			b.ReportMetric(float64(timeoutCount), "timeouts")
		})
	}
}

// BenchmarkOpenBaoSecretRotation measures overhead of secret rotation
func BenchmarkOpenBaoSecretRotation(b *testing.B) {
	client := NewMockOpenBaoClient(1*time.Millisecond, true)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ctx := context.Background()

		// Simulate secret access pattern
		path := fmt.Sprintf("seam/routes/service%d", i%10)

		// First access - cache miss
		secret1, err := client.GetSecret(ctx, path)
		if err != nil {
			b.Fatalf("GetSecret failed: %v", err)
		}

		// Simulate cache invalidation (rotation)
		client.mu.Lock()
		delete(client.secretCache, path)
		client.mu.Unlock()

		// Second access - cache miss again (new secret)
		secret2, err := client.GetSecret(ctx, path)
		if err != nil {
			b.Fatalf("GetSecret failed after rotation: %v", err)
		}

		// The mock derives both secrets from the path, so they are equal here;
		// in a real rotation they would differ. This benchmark only measures
		// the re-fetch overhead, so no assertion is made.
		_, _ = secret1, secret2
	}

	requests, hits, _ := client.GetMetrics()
	rotationOverhead := float64(requests) - float64(hits)/float64(requests)*100
	b.ReportMetric(rotationOverhead, "rotation_overhead%%")
}

// BenchmarkOpenBaoWarming measures cache warmup performance
func BenchmarkOpenBaoWarming(b *testing.B) {
	secretCounts := []int{10, 50, 100, 500}

	for _, count := range secretCounts {
		b.Run(fmt.Sprintf("Warmup-%dSecrets", count), func(b *testing.B) {
			client := NewMockOpenBaoClient(1*time.Millisecond, true)

			b.ResetTimer()
			b.ReportAllocs()

			// Warmup phase - populate cache
			for i := 0; i < count; i++ {
				ctx := context.Background()
				path := fmt.Sprintf("seam/routes/secret%d", i)
				_, err := client.GetSecret(ctx, path)
				if err != nil {
					b.Fatalf("GetSecret failed: %v", err)
				}
			}

			b.StopTimer()

			// Measure warmup overhead
			requests, _, _ := client.GetMetrics()
			warmupRequests := float64(requests) / float64(count)
			b.ReportMetric(warmupRequests, "req_per_secret")

			// Now measure cached access performance
			b.StartTimer()

			for i := 0; i < b.N; i++ {
				ctx := context.Background()
				path := fmt.Sprintf("seam/routes/secret%d", i%count)
				_, err := client.GetSecret(ctx, path)
				if err != nil {
					b.Fatalf("GetSecret failed: %v", err)
				}
			}

			requests, hits, _ := client.GetMetrics()
			hitRate := float64(hits) / float64(requests) * 100
			b.ReportMetric(hitRate, "warm_hit_rate%%")
		})
	}
}

// BenchmarkOpenBaoStressTest measures OpenBao client under extreme load
func BenchmarkOpenBaoStressTest(b *testing.B) {
	client := NewMockOpenBaoClient(1*time.Millisecond, true)

	b.ResetTimer()
	b.ReportAllocs()

	done := make(chan struct{})
	concurrency := 100
	requestsPerGoroutine := b.N / concurrency

	// Launch many concurrent goroutines
	for i := 0; i < concurrency; i++ {
		go func(id int) {
			for j := 0; j < requestsPerGoroutine; j++ {
				ctx := context.Background()
				path := fmt.Sprintf("seam/routes/test%d", (id+j)%1000)
				_, err := client.GetSecret(ctx, path)
				if err != nil {
					b.Errorf("GetSecret failed: %v", err)
				}
			}
			done <- struct{}{}
		}(i)
	}

	// Wait for completion
	for i := 0; i < concurrency; i++ {
		<-done
	}

	requests, hits, _ := client.GetMetrics()
	totalRequests := float64(requests)
	hitRate := float64(hits) / totalRequests * 100

	b.ReportMetric(hitRate, "hit_rate%%")
	b.ReportMetric(totalRequests, "total_requests")
}
