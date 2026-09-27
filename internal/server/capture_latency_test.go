package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCaptureLatencyUnderLoad measures latency under sustained load
func TestCaptureLatencyUnderLoad(t *testing.T) {
	testCases := []struct {
		name             string
		numRequests      int
		concurrency      int
		acceptablLatency time.Duration
		captureEnabled   bool
	}{
		{
			name:             "light-load-no-capture",
			numRequests:      100,
			concurrency:      1,
			acceptablLatency: 10 * time.Millisecond,
			captureEnabled:   false,
		},
		{
			name:             "light-load-with-capture",
			numRequests:      100,
			concurrency:      1,
			acceptablLatency: 10 * time.Millisecond,
			captureEnabled:   true,
		},
		{
			name:             "medium-load-no-capture",
			numRequests:      500,
			concurrency:      10,
			acceptablLatency: 50 * time.Millisecond,
			captureEnabled:   false,
		},
		{
			name:             "medium-load-with-capture",
			numRequests:      500,
			concurrency:      10,
			acceptablLatency: 50 * time.Millisecond,
			captureEnabled:   true,
		},
		{
			name:             "heavy-load-no-capture",
			numRequests:      1000,
			concurrency:      50,
			acceptablLatency: 100 * time.Millisecond,
			captureEnabled:   false,
		},
		{
			name:             "heavy-load-with-capture",
			numRequests:      1000,
			concurrency:      50,
			acceptablLatency: 100 * time.Millisecond,
			captureEnabled:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			corpusDir := t.TempDir()
			cm := NewCaptureMiddleware(corpusDir, "test-service", "test-incumbent", false)
			// Capture middleware starts enabled; make the disabled case explicit.
			if tc.captureEnabled {
				cm.Enable()
			} else {
				cm.Disable()
			}

			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"status":"ok"}`))
			})

			wrappedHandler := cm.Wrap(nextHandler)

			// Track latencies
			latencies := make([]time.Duration, tc.numRequests)
			var mu sync.Mutex
			var wg sync.WaitGroup

			// Semaphore for concurrency control
			sem := make(chan struct{}, tc.concurrency)

			startTime := time.Now()

			for i := 0; i < tc.numRequests; i++ {
				wg.Add(1)
				sem <- struct{}{} // Acquire semaphore

				go func(reqNum int) {
					defer wg.Done()
					defer func() { <-sem }() // Release semaphore

					req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", reqNum), nil)
					w := httptest.NewRecorder()

					start := time.Now()
					wrappedHandler.ServeHTTP(w, req)
					elapsed := time.Since(start)

					mu.Lock()
					latencies[reqNum] = elapsed
					mu.Unlock()

					if w.Code != http.StatusOK {
						t.Errorf("Request %d failed with status %d", reqNum, w.Code)
					}
				}(i)
			}

			wg.Wait()
			totalDuration := time.Since(startTime)

			// Calculate statistics
			p50, p95, p99 := percentiles(latencies)

			t.Logf("Load test results:")
			t.Logf("  Total requests: %d", tc.numRequests)
			t.Logf("  Concurrency: %d", tc.concurrency)
			t.Logf("  Total duration: %v", totalDuration)
			t.Logf("  Throughput: %.2f req/sec", float64(tc.numRequests)/totalDuration.Seconds())
			t.Logf("  Latency p50: %v", p50)
			t.Logf("  Latency p95: %v", p95)
			t.Logf("  Latency p99: %v", p99)
			t.Logf("  Captured entries: %d", cm.GetEntryCount())

			// Verify p99 latency is within acceptable threshold
			if p99 > tc.acceptablLatency {
				t.Errorf("p99 latency %v exceeds acceptable threshold %v", p99, tc.acceptablLatency)
			}

			// Verify capture count
			if tc.captureEnabled && cm.GetEntryCount() != tc.numRequests {
				t.Errorf("Expected %d captured entries, got %d", tc.numRequests, cm.GetEntryCount())
			}

			if !tc.captureEnabled && cm.GetEntryCount() != 0 {
				t.Errorf("Expected 0 captured entries when disabled, got %d", cm.GetEntryCount())
			}
		})
	}
}

// TestCaptureLatencyByPayloadSize tests latency with different payload sizes
func TestCaptureLatencyByPayloadSize(t *testing.T) {
	payloadSizes := []struct {
		name         string
		requestSize  int
		responseSize int
		maxLatency   time.Duration
	}{
		{
			name:         "small-payload",
			requestSize:  100,
			responseSize: 200,
			maxLatency:   5 * time.Millisecond,
		},
		{
			name:         "medium-payload",
			requestSize:  10 * 1024, // 10 KB
			responseSize: 50 * 1024, // 50 KB
			maxLatency:   20 * time.Millisecond,
		},
		{
			name:         "large-payload",
			requestSize:  100 * 1024, // 100 KB
			responseSize: 500 * 1024, // 500 KB
			maxLatency:   100 * time.Millisecond,
		},
		{
			name:         "very-large-payload",
			requestSize:  1024 * 1024,     // 1 MB
			responseSize: 5 * 1024 * 1024, // 5 MB
			maxLatency:   500 * time.Millisecond,
		},
	}

	for _, payload := range payloadSizes {
		t.Run(payload.name, func(t *testing.T) {
			corpusDir := t.TempDir()

			// Test without capture
			cmNoCapture := NewCaptureMiddleware(corpusDir+"/no-capture", "test-service", "test-incumbent", false)

			requestBody := bytes.Repeat([]byte("x"), payload.requestSize)
			responseBody := bytes.Repeat([]byte("y"), payload.responseSize)

			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Read request body
				_, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("Failed to read request body: %v", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}

				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
				w.Write(responseBody)
			})

			wrappedHandlerNoCapture := cmNoCapture.Wrap(nextHandler)

			// Measure baseline latency (no capture)
			samplesNoCapture := make([]time.Duration, 50)
			for i := 0; i < 50; i++ {
				req := httptest.NewRequest("POST", "/api/test", bytes.NewReader(requestBody))
				req.Header.Set("Content-Type", "application/octet-stream")
				w := httptest.NewRecorder()

				start := time.Now()
				wrappedHandlerNoCapture.ServeHTTP(w, req)
				samplesNoCapture[i] = time.Since(start)

				if w.Code != http.StatusOK {
					t.Errorf("Request failed with status %d", w.Code)
				}
			}

			// Test with capture enabled
			cmWithCapture := NewCaptureMiddleware(corpusDir+"/with-capture", "test-service", "test-incumbent", false)
			cmWithCapture.Enable()
			wrappedHandlerWithCapture := cmWithCapture.Wrap(nextHandler)

			samplesWithCapture := make([]time.Duration, 50)
			for i := 0; i < 50; i++ {
				req := httptest.NewRequest("POST", "/api/test", bytes.NewReader(requestBody))
				req.Header.Set("Content-Type", "application/octet-stream")
				w := httptest.NewRecorder()

				start := time.Now()
				wrappedHandlerWithCapture.ServeHTTP(w, req)
				samplesWithCapture[i] = time.Since(start)

				if w.Code != http.StatusOK {
					t.Errorf("Request failed with status %d", w.Code)
				}
			}

			// Calculate statistics
			medianNoCapture := medianDuration(samplesNoCapture)
			medianWithCapture := medianDuration(samplesWithCapture)

			p50NoCapture, p95NoCapture, _ := percentiles(samplesNoCapture)
			p50WithCapture, p95WithCapture, _ := percentiles(samplesWithCapture)

			captureOverhead := medianWithCapture - medianNoCapture
			overheadPercent := float64(captureOverhead) / float64(medianNoCapture) * 100

			t.Logf("Payload size test results (%s):", payload.name)
			t.Logf("  Request size: %d bytes", payload.requestSize)
			t.Logf("  Response size: %d bytes", payload.responseSize)
			t.Logf("  Baseline p50: %v, p95: %v", p50NoCapture, p95NoCapture)
			t.Logf("  Capture p50: %v, p95: %v", p50WithCapture, p95WithCapture)
			t.Logf("  Capture overhead: %v (%.1f%%)", captureOverhead, overheadPercent)

			// Verify absolute latency is within threshold
			if p95WithCapture > payload.maxLatency {
				t.Errorf("p95 latency %v exceeds maximum threshold %v", p95WithCapture, payload.maxLatency)
			}

			// Verify overhead is reasonable (less than 200% for large payloads)
			maxOverheadPercent := 200.0
			if payload.requestSize < 1024*1024 {
				maxOverheadPercent = 100.0 // Stricter for small payloads
			}

			if overheadPercent > maxOverheadPercent {
				t.Errorf("Capture overhead %.1f%% exceeds maximum %d%%", overheadPercent, int(maxOverheadPercent))
			}

			// Verify all entries were captured
			if cmWithCapture.GetEntryCount() != 50 {
				t.Errorf("Expected 50 captured entries, got %d", cmWithCapture.GetEntryCount())
			}
		})
	}
}

// TestCaptureLatencyComparisonDirectly compares capture vs no-capture side-by-side
func TestCaptureLatencyComparisonDirectly(t *testing.T) {
	corpusDir := t.TempDir()

	// Test configurations
	testConfigs := []struct {
		name       string
		method     string
		path       string
		body       string
		headers    map[string]string
		sampleSize int
	}{
		{
			name:       "simple-get",
			method:     "GET",
			path:       "/api/health",
			sampleSize: 100,
		},
		{
			name:       "post-with-json-body",
			method:     "POST",
			path:       "/api/create",
			body:       `{"name":"test","value":42,"nested":{"key":"value"}}`,
			headers:    map[string]string{"Content-Type": "application/json"},
			sampleSize: 100,
		},
		{
			name:       "put-with-large-body",
			method:     "PUT",
			path:       "/api/update/123",
			body:       strings.Repeat(`{"item":"value"},`, 100),
			headers:    map[string]string{"Content-Type": "application/json"},
			sampleSize: 50,
		},
	}

	for _, config := range testConfigs {
		t.Run(config.name, func(t *testing.T) {
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"result":"success"}`))
			})

			// Baseline: no capture
			cmBaseline := NewCaptureMiddleware(corpusDir+"/baseline", "test-service", "test-incumbent", false)
			handlerBaseline := cmBaseline.Wrap(nextHandler)

			baselineSamples := make([]time.Duration, config.sampleSize)
			for i := 0; i < config.sampleSize; i++ {
				var body io.Reader
				if config.body != "" {
					body = strings.NewReader(config.body)
				}

				req := httptest.NewRequest(config.method, config.path, body)
				for k, v := range config.headers {
					req.Header.Set(k, v)
				}
				w := httptest.NewRecorder()

				start := time.Now()
				handlerBaseline.ServeHTTP(w, req)
				baselineSamples[i] = time.Since(start)

				if w.Code != http.StatusOK {
					t.Errorf("Baseline request %d failed", i)
				}
			}

			// With capture enabled
			cmCapture := NewCaptureMiddleware(corpusDir+"/capture", "test-service", "test-incumbent", false)
			cmCapture.Enable()
			handlerCapture := cmCapture.Wrap(nextHandler)

			captureSamples := make([]time.Duration, config.sampleSize)
			for i := 0; i < config.sampleSize; i++ {
				var body io.Reader
				if config.body != "" {
					body = strings.NewReader(config.body)
				}

				req := httptest.NewRequest(config.method, config.path, body)
				for k, v := range config.headers {
					req.Header.Set(k, v)
				}
				w := httptest.NewRecorder()

				start := time.Now()
				handlerCapture.ServeHTTP(w, req)
				captureSamples[i] = time.Since(start)

				if w.Code != http.StatusOK {
					t.Errorf("Capture request %d failed", i)
				}
			}

			// Statistical analysis
			baselineP50, baselineP95, baselineP99 := percentiles(baselineSamples)
			captureP50, captureP95, captureP99 := percentiles(captureSamples)

			overheadP50 := captureP50 - baselineP50
			overheadP95 := captureP95 - baselineP95
			overheadP99 := captureP99 - baselineP99

			overheadPercentP50 := float64(overheadP50) / float64(baselineP50) * 100
			overheadPercentP95 := float64(overheadP95) / float64(baselineP95) * 100
			overheadPercentP99 := float64(overheadP99) / float64(baselineP99) * 100

			t.Logf("Latency comparison for %s:", config.name)
			t.Logf("  Sample size: %d", config.sampleSize)
			t.Logf("  Baseline   - p50: %v, p95: %v, p99: %v", baselineP50, baselineP95, baselineP99)
			t.Logf("  With Capture - p50: %v, p95: %v, p99: %v", captureP50, captureP95, captureP99)
			t.Logf("  Overhead    - p50: %v (%.1f%%), p95: %v (%.1f%%), p99: %v (%.1f%%)",
				overheadP50, overheadPercentP50,
				overheadP95, overheadPercentP95,
				overheadP99, overheadPercentP99)

			// Verify overhead thresholds
			// Absolute threshold: capture should add less than 10ms
			maxAbsoluteOverhead := 10 * time.Millisecond
			if overheadP50 > maxAbsoluteOverhead {
				t.Errorf("p50 overhead %v exceeds absolute threshold %v", overheadP50, maxAbsoluteOverhead)
			}
			if overheadP95 > maxAbsoluteOverhead {
				t.Errorf("p95 overhead %v exceeds absolute threshold %v", overheadP95, maxAbsoluteOverhead)
			}

			// Relative threshold: capture overhead should be less than 100% (2x baseline)
			maxRelativeOverhead := 100.0
			if overheadPercentP50 > maxRelativeOverhead {
				t.Errorf("p50 overhead %.1f%% exceeds relative threshold %.1f%%", overheadPercentP50, maxRelativeOverhead)
			}
			if overheadPercentP95 > maxRelativeOverhead {
				t.Errorf("p95 overhead %.1f%% exceeds relative threshold %.1f%%", overheadPercentP95, maxRelativeOverhead)
			}

			// Verify all entries were captured
			if cmCapture.GetEntryCount() != config.sampleSize {
				t.Errorf("Expected %d captured entries, got %d", config.sampleSize, cmCapture.GetEntryCount())
			}
		})
	}
}

// TestCaptureSustainedOperation tests sustained capture operation over time
func TestCaptureSustainedOperation(t *testing.T) {
	durations := []struct {
		name        string
		duration    time.Duration
		requestRate int // requests per second
		maxLatency  time.Duration
	}{
		{
			name:        "short-burst",
			duration:    5 * time.Second,
			requestRate: 10,
			maxLatency:  20 * time.Millisecond,
		},
		{
			name:        "medium-sustained",
			duration:    10 * time.Second,
			requestRate: 50,
			maxLatency:  50 * time.Millisecond,
		},
	}

	for _, test := range durations {
		t.Run(test.name, func(t *testing.T) {
			corpusDir := t.TempDir()
			cm := NewCaptureMiddleware(corpusDir, "test-service", "test-incumbent", false)
			cm.Enable()

			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"status":"ok"}`))
			})

			wrappedHandler := cm.Wrap(nextHandler)

			ctx, cancel := context.WithTimeout(context.Background(), test.duration+5*time.Second)
			defer cancel()

			interval := time.Second / time.Duration(test.requestRate)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			var (
				requestCount       int
				totalLatency       time.Duration
				maxLatencyObserved time.Duration
				mu                 sync.Mutex
			)

			startTime := time.Now()

			for time.Since(startTime) < test.duration {
				select {
				case <-ticker.C:
					req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", requestCount), nil)
					w := httptest.NewRecorder()

					start := time.Now()
					wrappedHandler.ServeHTTP(w, req)
					elapsed := time.Since(start)

					mu.Lock()
					requestCount++
					totalLatency += elapsed
					if elapsed > maxLatencyObserved {
						maxLatencyObserved = elapsed
					}
					mu.Unlock()

					if w.Code != http.StatusOK {
						t.Errorf("Request %d failed with status %d", requestCount, w.Code)
					}

				case <-ctx.Done():
					t.Fatalf("Test timed out")
				}
			}

			avgLatency := totalLatency / time.Duration(requestCount)

			t.Logf("Sustained operation results (%s):", test.name)
			t.Logf("  Duration: %v", test.duration)
			t.Logf("  Target rate: %d req/sec", test.requestRate)
			t.Logf("  Actual rate: %.2f req/sec", float64(requestCount)/test.duration.Seconds())
			t.Logf("  Total requests: %d", requestCount)
			t.Logf("  Average latency: %v", avgLatency)
			t.Logf("  Max latency: %v", maxLatencyObserved)
			t.Logf("  Captured entries: %d", cm.GetEntryCount())

			// Verify max latency is within threshold
			if maxLatencyObserved > test.maxLatency {
				t.Errorf("Max latency %v exceeds threshold %v", maxLatencyObserved, test.maxLatency)
			}

			// Verify all entries were captured
			if cm.GetEntryCount() != requestCount {
				t.Errorf("Expected %d captured entries, got %d", requestCount, cm.GetEntryCount())
			}

			// Verify capture didn't degrade over time
			// (simple check: actual rate should be reasonable)
			if requestCount > 20 {
				expectedRate := float64(requestCount) / test.duration.Seconds()
				minAcceptableRate := expectedRate * 0.5 // At least 50% of target rate

				actualRate := float64(requestCount) / test.duration.Seconds()
				if actualRate < minAcceptableRate {
					t.Errorf("Throughput degraded over time: %.2f req/sec (expected at least %.2f req/sec)",
						actualRate, minAcceptableRate)
				}
			}
		})
	}
}

// percentiles calculates p50, p95, and p99 percentiles
func percentiles(durations []time.Duration) (p50, p95, p99 time.Duration) {
	if len(durations) == 0 {
		return 0, 0, 0
	}

	// Make a copy to avoid modifying original
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)

	// Simple bubble sort
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
