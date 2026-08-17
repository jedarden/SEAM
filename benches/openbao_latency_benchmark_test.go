package benches

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/testutil/openbao"
)

// BenchmarkResult captures metrics for a single benchmark run
type BenchmarkResult struct {
	Name           string        `json:"name"`
	Iterations     int           `json:"iterations"`
	NsPerOp        float64       `json:"ns_per_op"`
	BytesPerOp     uint64        `json:"bytes_per_op"`
	AllocsPerOp    uint64        `json:"allocs_per_op"`
	CacheHitRate   float64       `json:"cache_hit_rate,omitempty"`
	Latency        time.Duration `json:"latency"`
	CredentialSize int           `json:"credential_size"`
}

// OpenBaoBenchmarkSuite manages OpenBao server lifecycle for benchmarks
type OpenBaoBenchmarkSuite struct {
	server  *openbao.Server
	client  *openbao.Client
	results []BenchmarkResult
}

// NewOpenBaoBenchmarkSuite creates a new benchmark suite with an OpenBao server
func NewOpenBaoBenchmarkSuite(t testing.TB) *OpenBaoBenchmarkSuite {
	// Check if OpenBao binary is available
	if _, err := openbao.NewClientForTesting(); err != nil {
		t.Skipf("OpenBao not available: %v", err)
	}

	cfg := openbao.ServerConfig{
		DevToken:   "bench-root-token",
		ListenAddr: "localhost:18201",
	}

	srv, err := openbao.NewServer(cfg)
	if err != nil {
		t.Fatalf("Failed to start OpenBao server: %v", err)
	}

	// Setup test secrets
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.SetupTestSecrets(ctx); err != nil {
		srv.Close()
		t.Fatalf("Failed to setup test secrets: %v", err)
	}

	t.Cleanup(func() {
		srv.Close()
	})

	return &OpenBaoBenchmarkSuite{
		server:  srv,
		client:  srv.Client(),
		results: make([]BenchmarkResult, 0),
	}
}

// BenchmarkOpenBaoColdCache measures secret retrieval latency with an empty cache
func BenchmarkOpenBaoColdCache(b *testing.B) {
	suite := NewOpenBaoBenchmarkSuite(b)

	credentialSizes := map[string]int{
		"small":  100,
		"medium": 1024,
		"large":  10240,
	}

	for sizeName, size := range credentialSizes {
		b.Run(fmt.Sprintf("Size-%s", sizeName), func(b *testing.B) {
			// Write a secret of the specified size
			ctx := context.Background()
			secretPath := fmt.Sprintf("bench/coldcache/%s", sizeName)

			// Create secret data of specified size
			largeValue := strings.Repeat("x", size)
			secretData := map[string]interface{}{
				"token": largeValue,
				"type":  "bearer",
			}

			if err := suite.client.WriteSecret(ctx, secretPath, secretData); err != nil {
				b.Fatalf("Failed to write secret: %v", err)
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				start := time.Now()
				_, err := suite.client.ReadSecret(ctx, secretPath)
				if err != nil {
					b.Fatalf("ReadSecret failed: %v", err)
				}
				elapsed := time.Since(start)

				// Report custom metric
				b.ReportMetric(float64(elapsed.Nanoseconds()), "ns/request")
			}

			// Save result
			suite.results = append(suite.results, BenchmarkResult{
				Name:           fmt.Sprintf("ColdCache-Size-%s", sizeName),
				CredentialSize: size,
				Latency:        0, // Will be calculated from b.N
			})
		})
	}
}

// BenchmarkOpenBaoWarmCache measures secret retrieval latency with a populated cache
func BenchmarkOpenBaoWarmCache(b *testing.B) {
	suite := NewOpenBaoBenchmarkSuite(b)

	credentialSizes := map[string]int{
		"small":  100,
		"medium": 1024,
		"large":  10240,
	}

	for sizeName, size := range credentialSizes {
		b.Run(fmt.Sprintf("Size-%s", sizeName), func(b *testing.B) {
			ctx := context.Background()
			secretPath := fmt.Sprintf("bench/warmcache/%s", sizeName)

			// Create and warm up cache
			largeValue := strings.Repeat("x", size)
			secretData := map[string]interface{}{
				"token": largeValue,
				"type":  "bearer",
			}

			if err := suite.client.WriteSecret(ctx, secretPath, secretData); err != nil {
				b.Fatalf("Failed to write secret: %v", err)
			}

			// Warm up by reading once
			if _, err := suite.client.ReadSecret(ctx, secretPath); err != nil {
				b.Fatalf("Failed to warm cache: %v", err)
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				start := time.Now()
				_, err := suite.client.ReadSecret(ctx, secretPath)
				if err != nil {
					b.Fatalf("ReadSecret failed: %v", err)
				}
				elapsed := time.Since(start)

				b.ReportMetric(float64(elapsed.Nanoseconds()), "ns/request")
			}
		})
	}
}

// BenchmarkOpenBaoCacheMiss measures latency when requesting non-existent secrets
func BenchmarkOpenBaoCacheMiss(b *testing.B) {
	suite := NewOpenBaoBenchmarkSuite(b)

	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		secretPath := fmt.Sprintf("bench/doesnotexist/secret%d", i)
		start := time.Now()
		_, err := suite.client.ReadSecret(ctx, secretPath)
		elapsed := time.Since(start)

		// We expect an error for non-existent secrets
		if err == nil {
			b.Fatalf("Expected error for non-existent secret, got nil")
		}

		b.ReportMetric(float64(elapsed.Nanoseconds()), "ns/request")
	}
}

// BenchmarkOpenBaoConcurrentAccess measures performance under concurrent load
func BenchmarkOpenBaoConcurrentAccess(b *testing.B) {
	suite := NewOpenBaoBenchmarkSuite(b)

	concurrencyLevels := []int{1, 10, 50, 100}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("Concurrent-%d", concurrency), func(b *testing.B) {
			ctx := context.Background()
			b.ResetTimer()
			b.ReportAllocs()

			b.RunParallel(func(pb *testing.PB) {
				secretPath := fmt.Sprintf("seam/routes/testservice/token")

				for pb.Next() {
					start := time.Now()
					_, err := suite.client.ReadSecret(ctx, secretPath)
					elapsed := time.Since(start)

					if err != nil {
						b.Errorf("ReadSecret failed: %v", err)
					}

					b.ReportMetric(float64(elapsed.Nanoseconds()), "ns/request")
				}
			})
		})
	}
}

// BenchmarkOpenBaoVariableSizedSecrets measures latency across different credential sizes
func BenchmarkOpenBaoVariableSizedSecrets(b *testing.B) {
	suite := NewOpenBaoBenchmarkSuite(b)

	sizes := []int{
		64,    // Very small
		256,   // Small
		1024,  // 1KB
		4096,  // 4KB
		16384, // 16KB
		65536, // 64KB
	}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("%d-bytes", size), func(b *testing.B) {
			ctx := context.Background()
			secretPath := fmt.Sprintf("bench/variable/%d", size)

			// Create secret of exact size
			largeValue := strings.Repeat("x", size)
			secretData := map[string]interface{}{
				"token": largeValue,
				"type":  "bearer",
			}

			if err := suite.client.WriteSecret(ctx, secretPath, secretData); err != nil {
				b.Fatalf("Failed to write secret: %v", err)
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				start := time.Now()
				_, err := suite.client.ReadSecret(ctx, secretPath)
				elapsed := time.Since(start)

				if err != nil {
					b.Fatalf("ReadSecret failed: %v", err)
				}

				b.ReportMetric(float64(elapsed.Nanoseconds()), "ns/request")
			}

			// Report size as custom metric
			b.ReportMetric(float64(size), "bytes")
		})
	}
}

// saveBenchmarkResults saves results to a JSON file
func saveBenchmarkResults(results []BenchmarkResult, filename string) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal results: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write results file: %w", err)
	}

	return nil
}

// BenchmarkOpenBaoComprehensive is a main benchmark that runs all scenarios
func BenchmarkOpenBaoComprehensive(b *testing.B) {
	suite := NewOpenBaoBenchmarkSuite(b)

	scenarios := []struct {
		name string
		fn   func(b *testing.B, suite *OpenBaoBenchmarkSuite)
	}{
		{"ColdCache", func(b *testing.B, s *OpenBaoBenchmarkSuite) {
			ctx := context.Background()
			for i := 0; i < b.N; i++ {
				path := fmt.Sprintf("bench/comprehensive/cold/%d", i%10)
				_, err := s.client.ReadSecret(ctx, path)
				if err != nil {
					// Expected for non-existent paths
				}
			}
		}},
		{"WarmCache", func(b *testing.B, s *OpenBaoBenchmarkSuite) {
			ctx := context.Background()
			path := "bench/comprehensive/warm/secret"
			for i := 0; i < b.N; i++ {
				_, err := s.client.ReadSecret(ctx, path)
				if err != nil {
					b.Fatalf("ReadSecret failed: %v", err)
				}
			}
		}},
		{"Concurrent", func(b *testing.B, s *OpenBaoBenchmarkSuite) {
			ctx := context.Background()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					path := fmt.Sprintf("bench/comprehensive/concurrent/%d", i%50)
					i++
					_, err := s.client.ReadSecret(ctx, path)
					if err != nil {
						// Expected for some non-existent paths
					}
				}
			})
		}},
	}

	for _, scenario := range scenarios {
		b.Run(scenario.name, func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()
			scenario.fn(b, suite)
		})
	}

	// Save results if in CI mode
	if os.Getenv("BENCHMARK_OUTPUT") != "" {
		if err := saveBenchmarkResults(suite.results, os.Getenv("BENCHMARK_OUTPUT")); err != nil {
			b.Errorf("Failed to save benchmark results: %v", err)
		}
	}
}

// TestOpenBaoBenchmarkIntegration verifies the benchmark infrastructure works
func TestOpenBaoBenchmarkIntegration(t *testing.T) {
	suite := NewOpenBaoBenchmarkSuite(t)

	ctx := context.Background()

	// Test basic secret operations
	testSecret := map[string]interface{}{
		"token": "benchmark-test-token",
		"type":  "bearer",
	}

	if err := suite.client.WriteSecret(ctx, "bench/integration/test", testSecret); err != nil {
		t.Fatalf("Failed to write test secret: %v", err)
	}

	retrieved, err := suite.client.ReadSecret(ctx, "bench/integration/test")
	if err != nil {
		t.Fatalf("Failed to read test secret: %v", err)
	}

	if retrieved["token"] != "benchmark-test-token" {
		t.Errorf("Expected token 'benchmark-test-token', got '%v'", retrieved["token"])
	}

	t.Log("OpenBao benchmark integration test passed")
}

// TestOpenBaoBenchmarkJSONOutput tests JSON output generation
func TestOpenBaoBenchmarkJSONOutput(t *testing.T) {
	results := []BenchmarkResult{
		{
			Name:           "TestBenchmark",
			Iterations:     1000,
			NsPerOp:        1234.5,
			BytesPerOp:     512,
			AllocsPerOp:    5,
			CredentialSize: 1024,
			Latency:        1234 * time.Nanosecond,
		},
	}

	tmpFile := "/tmp/benchmark-test-output.json"
	defer os.Remove(tmpFile)

	if err := saveBenchmarkResults(results, tmpFile); err != nil {
		t.Fatalf("Failed to save results: %v", err)
	}

	// Verify file can be read and parsed
	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("Failed to read results file: %v", err)
	}

	var parsed []BenchmarkResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to parse results JSON: %v", err)
	}

	if len(parsed) != 1 {
		t.Errorf("Expected 1 result, got %d", len(parsed))
	}

	if parsed[0].Name != "TestBenchmark" {
		t.Errorf("Expected name 'TestBenchmark', got '%s'", parsed[0].Name)
	}

	t.Log("JSON output test passed")
}
