package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/limits"
	"github.com/ardenone/seam/internal/spec"
	"github.com/pb33f/libopenapi"
)

func TestRouteLimitLoaderEnforcesOperatorCeilings(t *testing.T) {
	const document = `{"openapi":"3.0.0","info":{"title":"limits","version":"1"},"x-max-request-bytes":4194304,"x-max-response-bytes":2097152,"x-timeout":"30s","paths":{"/large":{"post":{"x-max-request-bytes":5242880,"x-timeout":"50s","responses":{"200":{"description":"ok"}}}}}}`
	doc, err := libopenapi.NewDocument([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatal(err)
	}
	ceilings := limits.Ceilings{RequestBytes: 8 << 20, ResponseBytes: 4 << 20, Timeout: time.Minute}
	table, err := buildRouteTableWithCeilings(&model.Model, nil, ceilings)
	if err != nil {
		t.Fatal(err)
	}
	routes := table.GetRoutes()
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want one", len(routes))
	}
	if routes[0].MaxRequestBytes != 5<<20 || routes[0].MaxResponseBytes != 2<<20 || routes[0].Timeout != 50*time.Second {
		t.Fatalf("effective route limits = %+v, want operation request/timeout and root response", routes[0])
	}
	for _, tc := range []struct {
		name     string
		ceilings limits.Ceilings
		want     string
	}{
		{"request", limits.Ceilings{RequestBytes: 4 << 20, ResponseBytes: 4 << 20, Timeout: time.Minute}, "x-max-request-bytes"},
		{"response", limits.Ceilings{RequestBytes: 8 << 20, ResponseBytes: 1 << 20, Timeout: time.Minute}, "x-max-response-bytes"},
		{"timeout", limits.Ceilings{RequestBytes: 8 << 20, ResponseBytes: 4 << 20, Timeout: 40 * time.Second}, "x-timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildRouteTableWithCeilings(&model.Model, nil, tc.ceilings); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("loader error = %v, want %s ceiling rejection", err, tc.want)
			}
		})
	}
}

func TestFragmentRootLimitsSurviveMerge(t *testing.T) {
	fragmentsDir := t.TempDir()
	serviceDir := filepath.Join(fragmentsDir, "limits")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const fragment = `{"x-seam-schema":"v1","x-seam-owner":"limits","x-max-request-bytes":4194304,"x-max-response-bytes":2097152,"x-timeout":"30s","paths":{"/limits":{"post":{"x-api-version":"v1","x-upstream":"https://upstream.example","x-max-request-bytes":5242880,"responses":{"200":{"description":"ok"}}}}}}`
	if err := os.WriteFile(filepath.Join(serviceDir, "route.json"), []byte(fragment), 0o644); err != nil {
		t.Fatal(err)
	}
	loader, err := spec.NewWithFragments(t.TempDir(), "http://localhost:8080", "", fragmentsDir)
	if err != nil {
		t.Fatal(err)
	}
	table, err := buildRouteTableWithCeilings(loader.OpenAPIModel(), nil, limits.Ceilings{RequestBytes: 8 << 20, ResponseBytes: 4 << 20, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, route := range table.GetRoutes() {
		if route.PathTemplate != "/limits" {
			continue
		}
		found = true
		if route.MaxRequestBytes != 5<<20 || route.MaxResponseBytes != 2<<20 || route.Timeout != 30*time.Second {
			t.Fatalf("merged route limits = %+v, want operation request and root response/timeout", route)
		}
	}
	if !found {
		t.Fatal("merged fragment route not found")
	}
}

func TestRouteRequestLimitRaisedAndOperatorCeiling(t *testing.T) {
	const secret = "route-limit-test-credential"
	var dispatches atomic.Int32
	var bodyBytes atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatches.Add(1)
		if got := r.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer "+secret {
			t.Errorf("upstream credential header count/value = %v, want one injected credential", got)
		}
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil && r.ContentLength >= 0 {
			t.Errorf("read upstream body: %v", err)
		}
		bodyBytes.Store(n)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxyWithConfig(upstream.URL, &ReverseProxyConfig{MaxRequestBytesCeiling: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var resolves atomic.Int32
	route := RouteEntry{PathTemplate: "/large", Method: http.MethodPost, MaxRequestBytes: 5 << 20,
		VaultPath: "secret/example/route-limit", InjectAs: &InjectAs{Kind: InjectionBearer}}
	resolver := routeSecretResolver(func(context.Context, RouteEntry) ([]byte, error) {
		resolves.Add(1)
		return []byte(secret), nil
	})
	request := func(size int, unknownLength bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/large", bytes.NewReader(bytes.Repeat([]byte{'x'}, size)))
		if unknownLength {
			r.ContentLength = -1
		}
		withRouteMatch(r, &RouteMatch{Route: route}, resolver)
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		return w
	}

	const accepted = 4 << 20
	if w := request(accepted, false); w.Code != http.StatusNoContent {
		t.Fatalf("4 MiB status = %d, want 204: %s", w.Code, w.Body.String())
	}
	if got := bodyBytes.Load(); got != accepted {
		t.Fatalf("forwarded bytes = %d, want %d", got, accepted)
	}
	if w := request((8<<20)+1, false); w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), string(ErrCodeRequestTooLarge)) {
		t.Fatalf("above-ceiling response = %d %q, want structured 413", w.Code, w.Body.String())
	}
	if got := dispatches.Load(); got != 1 {
		t.Fatalf("upstream dispatches after known-length ceiling refusal = %d, want only accepted request", got)
	}
	if got := resolves.Load(); got != 1 {
		t.Fatalf("credential resolutions after known-length ceiling refusal = %d, want only accepted request", got)
	}
	if w := request((8<<20)+1, true); w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), string(ErrCodeRequestTooLarge)) {
		t.Fatalf("unknown-length above-ceiling response = %d %q, want structured 413", w.Code, w.Body.String())
	}
	// An unknown-length request is streamed as it arrives. The upstream may
	// receive a prefix before the ceiling is crossed; the caller still gets
	// 413 and the body cannot be replayed.
	if got := dispatches.Load(); got != 2 {
		t.Fatalf("upstream dispatches = %d, want accepted request plus partial unknown-length stream", got)
	}
}

func TestRouteOversizeStreamingCannotReplayOn401(t *testing.T) {
	const capBytes = 4 << 20
	bigBody := bytes.Repeat([]byte{'z'}, 5<<20)
	var dispatches atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatches.Add(1)
		if auth := r.Header.Values("Authorization"); len(auth) != 1 || auth[0] != "Bearer "+phase12StaleSecret {
			t.Errorf("injected credentials = %v, want exactly one stale credential", auth)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(body, bigBody) {
			t.Errorf("streamed body length = %d, read error = %v, want complete %d bytes", len(body), err, len(bigBody))
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	proxy, err := NewReverseProxyWithConfig(upstream.URL, &ReverseProxyConfig{MaxRequestBytesCeiling: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var resolves atomic.Int32
	req := httptest.NewRequest(http.MethodPost, "/large", bytes.NewReader(bigBody))
	req.Header.Set("Authorization", "Bearer caller-supplied")
	// The process default remains 1 MiB while this route permits a 4 MiB
	// replay buffer. The 5 MiB request must still take the streaming path.
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{PathTemplate: "/large", Method: http.MethodPost,
		MaxRequestBytes: capBytes, VaultPath: "secret/example/large", InjectAs: &InjectAs{Kind: InjectionBearer}}},
		routeSecretResolver(func(context.Context, RouteEntry) ([]byte, error) {
			if resolves.Add(1) == 1 {
				return []byte(phase12StaleSecret), nil
			}
			return []byte(phase12FreshSecret), nil
		}))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), string(ErrCodeCredentialRefreshNotRetried)) {
		t.Fatalf("oversize 401 response = %d %q, want non-replayed envelope", rec.Code, rec.Body.String())
	}
	if got := resolves.Load(); got != 2 {
		t.Fatalf("credential resolver calls = %d, want initial and refresh", got)
	}
	if got := dispatches.Load(); got != 1 {
		t.Fatalf("upstream dispatches = %d, want one without replay", got)
	}
}

func TestRouteTimeoutOverride(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(150 * time.Millisecond):
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer upstream.Close()
	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/slow", nil)
	withRouteMatch(r, &RouteMatch{Route: RouteEntry{PathTemplate: "/slow", Method: http.MethodGet, Timeout: 20 * time.Millisecond}}, nil)
	w := httptest.NewRecorder()
	start := time.Now()
	proxy.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway || time.Since(start) > 120*time.Millisecond {
		t.Fatalf("route timeout response = %d after %s, want prompt gateway error", w.Code, time.Since(start))
	}
}

func TestRouteResponseBufferOverride(t *testing.T) {
	secret := []byte("route-response-secret")
	body := append(bytes.Repeat([]byte{'a'}, 2<<20), secret...)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	proxy, err := NewReverseProxyWithConfig(upstream.URL, &ReverseProxyConfig{
		MaxBufferedResponseBytes: 1 << 20, MaxResponseBytesCeiling: 4 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/response", nil)
	withRouteMatch(r, &RouteMatch{Route: RouteEntry{PathTemplate: "/response", Method: http.MethodGet,
		MaxResponseBytes: 3 << 20, InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Key"}}},
		func(context.Context, RouteEntry) ([]byte, error) { return secret, nil })
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	want := ScrubBytes(body, secret)
	if !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatal("route-buffered response was changed or not scrubbed")
	}
	if got := w.Header().Get("Content-Length"); got != itoa(len(want)) {
		t.Fatalf("Content-Length = %q, want buffered response length %d", got, len(want))
	}
}

func TestLoopGuardHashesOversizeStreamRawWithoutPrefixLoss(t *testing.T) {
	const capBytes = 1024
	for _, unknownLength := range []bool{false, true} {
		name := "known length"
		if unknownLength {
			name = "unknown length"
		}
		t.Run(name, func(t *testing.T) {
			body := bytes.Repeat([]byte{'q'}, capBytes+257)
			body[len(body)-1] = 'z' // Distinguishes the full stream from its prefix.
			route := RouteEntry{PathTemplate: "/hash-large", Method: http.MethodPost, APIVersion: "v1", MaxRequestBytes: capBytes,
				LoopGuardConfig: &LoopGuardConfig{MaxRepeats: 5, Window: "1m"}}
			s := &Server{config: &Config{MaxReplayableRequestBytes: capBytes}, loopGuardRegistry: NewLoopGuardRegistry()}
			r := httptest.NewRequest(http.MethodPost, "/hash-large", bytes.NewReader(body))
			if unknownLength {
				r.ContentLength = -1
			}
			withRouteMatch(r, &RouteMatch{Route: route}, nil)
			var read int64
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				read, err = io.Copy(io.Discard, r.Body)
				if err != nil {
					t.Errorf("consume streamed body: %v", err)
				}
				w.WriteHeader(http.StatusInternalServerError)
			})
			w := httptest.NewRecorder()
			s.LoopGuardMiddleware(next).ServeHTTP(w, r)
			if read != int64(len(body)) {
				t.Fatalf("streamed bytes = %d, want %d", read, len(body))
			}
			guard := s.loopGuardRegistry.GetGuard(route.ID())
			if guard == nil {
				t.Fatal("missing loop guard")
			}
			want := NewRequestHasher(capBytes).ComputeHash(http.MethodPost, route.PathTemplate, nil, nil, body)
			tracks := guard.Snapshot()["hash_tracks"].(map[string]interface{})
			if _, ok := tracks[want]; !ok {
				t.Fatalf("raw stream hash %s absent from tracks %v", want, tracks)
			}
		})
	}
}
