package server

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sort"
	"testing"
	"time"
)

const proxyCaptureLatencyBudget = 10 * time.Millisecond

type proxyCaptureLatencyCase struct {
	name          string
	requestBytes  int
	responseBytes int
}

type proxyCaptureLatencyFixture struct {
	client  *http.Client
	url     string
	capture *CaptureMiddleware
}

// newProxyCaptureLatencyFixture builds the same request path used by the
// production server: an HTTP client talks to a reverse proxy, which forwards
// to an upstream handler and optionally passes through capture middleware.
func newProxyCaptureLatencyFixture(tb testing.TB, requestBytes int, responseBody []byte, captureEnabled bool) *proxyCaptureLatencyFixture {
	tb.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestLength, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, "failed to read request", http.StatusBadRequest)
			return
		}
		if requestLength != int64(requestBytes) {
			http.Error(w, "request body was not forwarded intact", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responseBody)
	}))
	tb.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		tb.Fatalf("parse upstream URL: %v", err)
	}

	capture := NewCaptureMiddleware(tb.TempDir(), "latency-test", "test-incumbent", false)
	if captureEnabled {
		capture.Enable()
	} else {
		// NewCaptureMiddleware defaults to enabled. Disable before Wrap so the
		// baseline really exercises the proxy without capture work.
		capture.Disable()
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxyServer := httptest.NewServer(capture.Wrap(proxy))
	tb.Cleanup(proxyServer.Close)

	transport := &http.Transport{
		MaxIdleConns:        2,
		MaxIdleConnsPerHost: 2,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}
	tb.Cleanup(transport.CloseIdleConnections)

	return &proxyCaptureLatencyFixture{
		client:  client,
		url:     proxyServer.URL,
		capture: capture,
	}
}

func (f *proxyCaptureLatencyFixture) request(payload []byte) (time.Duration, int, error) {
	req, err := http.NewRequest(http.MethodPost, f.url+"/capture-latency", bytes.NewReader(payload))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	started := time.Now()
	resp, err := f.client.Do(req)
	elapsed := time.Since(started)
	if err != nil {
		return elapsed, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, readErr := io.Copy(io.Discard, resp.Body)
	if readErr != nil {
		return elapsed, resp.StatusCode, readErr
	}
	return elapsed, resp.StatusCode, nil
}

func measurePairedProxyCaptureLatency(t *testing.T, baseline, captured *proxyCaptureLatencyFixture, payload []byte, samples int) ([]time.Duration, []time.Duration) {
	t.Helper()

	const warmupRequests = 4
	for i := 0; i < warmupRequests; i++ {
		if _, status, err := baseline.request(payload); err != nil || status != http.StatusOK {
			t.Fatalf("baseline warmup request %d failed (status %d): %v", i, status, err)
		}
		if _, status, err := captured.request(payload); err != nil || status != http.StatusOK {
			t.Fatalf("capture warmup request %d failed (status %d): %v", i, status, err)
		}
	}

	baselineLatencies := make([]time.Duration, samples)
	captureLatencies := make([]time.Duration, samples)
	for i := 0; i < samples; i++ {
		// Alternate the order so a change in host load does not consistently
		// favor one mode over the other.
		if i%2 == 0 {
			baselineLatencies[i] = checkedProxyCaptureRequest(t, baseline, payload, "baseline", i)
			captureLatencies[i] = checkedProxyCaptureRequest(t, captured, payload, "capture", i)
		} else {
			captureLatencies[i] = checkedProxyCaptureRequest(t, captured, payload, "capture", i)
			baselineLatencies[i] = checkedProxyCaptureRequest(t, baseline, payload, "baseline", i)
		}
	}
	return baselineLatencies, captureLatencies
}

func checkedProxyCaptureRequest(t *testing.T, fixture *proxyCaptureLatencyFixture, payload []byte, mode string, requestNumber int) time.Duration {
	t.Helper()
	elapsed, status, err := fixture.request(payload)
	if err != nil {
		t.Fatalf("%s request %d failed: %v", mode, requestNumber, err)
	}
	if status != http.StatusOK {
		t.Fatalf("%s request %d returned status %d", mode, requestNumber, status)
	}
	return elapsed
}

// TestProxyCaptureLatencyByPayloadSize measures the end-to-end proxy path with
// capture enabled and disabled. The p95 difference is the regression signal:
// capture must add less than the documented 10 ms per request budget.
func TestProxyCaptureLatencyByPayloadSize(t *testing.T) {
	cases := []proxyCaptureLatencyCase{
		{name: "small", requestBytes: 128, responseBytes: 256},
		{name: "medium", requestBytes: 16 * 1024, responseBytes: 64 * 1024},
		{name: "large", requestBytes: 128 * 1024, responseBytes: 512 * 1024},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload := bytes.Repeat([]byte("r"), testCase.requestBytes)
			responseBody := bytes.Repeat([]byte("s"), testCase.responseBytes)
			baseline := newProxyCaptureLatencyFixture(t, testCase.requestBytes, responseBody, false)
			captured := newProxyCaptureLatencyFixture(t, testCase.requestBytes, responseBody, true)

			baselineLatencies, captureLatencies := measurePairedProxyCaptureLatency(t, baseline, captured, payload, 40)
			baselineP50 := proxyCapturePercentile(baselineLatencies, 0.50)
			baselineP95 := proxyCapturePercentile(baselineLatencies, 0.95)
			captureP50 := proxyCapturePercentile(captureLatencies, 0.50)
			captureP95 := proxyCapturePercentile(captureLatencies, 0.95)
			overheadP50 := captureP50 - baselineP50
			overheadP95 := captureP95 - baselineP95

			t.Logf("payload=%s request=%dB response=%dB", testCase.name, testCase.requestBytes, testCase.responseBytes)
			t.Logf("baseline p50=%v p95=%v; capture p50=%v p95=%v", baselineP50, baselineP95, captureP50, captureP95)
			t.Logf("capture overhead p50=%v p95=%v; budget=%v", overheadP50, overheadP95, proxyCaptureLatencyBudget)

			if overheadP95 > proxyCaptureLatencyBudget {
				t.Errorf("capture p95 overhead %v exceeds %v budget", overheadP95, proxyCaptureLatencyBudget)
			}
			if baselineP95 > 0 && captureP95 > baselineP95+proxyCaptureLatencyBudget {
				t.Errorf("capture p95 %v exceeds baseline p95 %v plus %v", captureP95, baselineP95, proxyCaptureLatencyBudget)
			}

			const warmupRequests = 4
			if got, want := captured.capture.GetEntryCount(), warmupRequests+40; got != want {
				t.Errorf("capture entry count = %d, want %d", got, want)
			}
			if got := baseline.capture.GetEntryCount(); got != 0 {
				t.Errorf("disabled capture recorded %d entries", got)
			}
		})
	}
}

func proxyCapturePercentile(samples []time.Duration, percentile float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(float64(len(sorted)) * percentile)
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

// muteCaptureLogs keeps benchmark output useful. Capture currently logs each
// entry, which is operationally helpful but would otherwise dominate a run.
func muteCaptureLogs(tb testing.TB) {
	tb.Helper()
	previous := log.Writer()
	log.SetOutput(io.Discard)
	tb.Cleanup(func() { log.SetOutput(previous) })
}

// BenchmarkProxyCaptureSustained measures steady-state proxy throughput for
// the same small request with capture enabled and disabled.
func BenchmarkProxyCaptureSustained(b *testing.B) {
	requestBody := bytes.Repeat([]byte("r"), 128)
	responseBody := bytes.Repeat([]byte("s"), 512)

	for _, captureEnabled := range []bool{false, true} {
		name := "capture-disabled"
		if captureEnabled {
			name = "capture-enabled"
		}
		b.Run(name, func(b *testing.B) {
			fixture := newProxyCaptureLatencyFixture(b, len(requestBody), responseBody, captureEnabled)
			if captureEnabled {
				muteCaptureLogs(b)
			}
			for i := 0; i < 4; i++ {
				if _, status, err := fixture.request(requestBody); err != nil || status != http.StatusOK {
					b.Fatalf("warmup request failed (status %d): %v", status, err)
				}
			}

			b.SetBytes(int64(len(requestBody) + len(responseBody)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, status, err := fixture.request(requestBody); err != nil || status != http.StatusOK {
					b.Fatalf("request %d failed (status %d): %v", i, status, err)
				}
			}
		})
	}
}

// BenchmarkProxyCaptureSustainedParallel measures concurrent steady-state
// capture, which exercises the middleware's entry lock under contention.
func BenchmarkProxyCaptureSustainedParallel(b *testing.B) {
	requestBody := bytes.Repeat([]byte("r"), 128)
	responseBody := bytes.Repeat([]byte("s"), 512)

	for _, captureEnabled := range []bool{false, true} {
		name := "capture-disabled"
		if captureEnabled {
			name = "capture-enabled"
		}
		b.Run(name, func(b *testing.B) {
			fixture := newProxyCaptureLatencyFixture(b, len(requestBody), responseBody, captureEnabled)
			if captureEnabled {
				muteCaptureLogs(b)
			}
			b.SetBytes(int64(len(requestBody) + len(responseBody)))
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, status, err := fixture.request(requestBody); err != nil || status != http.StatusOK {
						b.Errorf("request failed (status %d): %v", status, err)
						return
					}
				}
			})
		})
	}
}
