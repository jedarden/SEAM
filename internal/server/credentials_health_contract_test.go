package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The executable form of the /health/credentials contract
// (docs/notes/credentials-health-contract.md). TestCredentialsHealthStateMatrix
// pins one row per published breaker state — the exact HTTP status, the
// content type and cache directives, the closed top-level key set, the
// credentials-availability shape, and the status/aggregate mapping. The
// composition is the production operator chain: stage 3 (identity resolution)
// outside the mux, the scope gate inside it.
//
// TestCredentialsHealthCallerAccessRejected pins the other half of the
// contract: the caller-facing surface never serves the sentinel. A fragment
// cannot claim the path (it is reserved in the spec loader), so the caller
// mux's proxy catch-all answers route_not_found, and a resolved caller that
// reaches the operator mux anyway is denied by the seam:ops:read scope gate.
func TestCredentialsHealthStateMatrix(t *testing.T) {
	openedAt := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)

	// aggregateBaseKeys are the fields every aggregate carries; the omitempty
	// trio appears only on a state that published it.
	aggregateBaseKeys := []string{"enabled", "state", "consecutive_failures"}
	aggregateOpenKeys := append(append([]string{}, aggregateBaseKeys...),
		"opened_at", "last_error", "retry_after_seconds")

	tests := []struct {
		name string
		// setup publishes exactly the breaker state under test.
		setup func(t *testing.T, s *Server)
		// wantTopKeys is the exact top-level body key set; circuit_breakers
		// joins it only when a per-origin record is published.
		wantTopKeys []string
		wantStatus  string
		// wantAggregateKeys is the exact key set of the aggregate object.
		wantAggregateKeys []string
		wantAggState      string
		wantAggEnabled    bool
		wantAggFailures   int
		wantOrigins       int
	}{
		{
			name:              "no_breaker_published",
			setup:             func(t *testing.T, s *Server) {},
			wantTopKeys:       []string{"status", "timestamp", "credentials", "circuit_breaker"},
			wantStatus:        "healthy",
			wantAggregateKeys: aggregateBaseKeys,
			wantAggState:      string(CircuitBreakerClosed),
			wantAggEnabled:    false,
			wantOrigins:       0,
		},
		{
			name: "breaker_closed",
			setup: func(t *testing.T, s *Server) {
				s.CircuitBreakerStates().Set(CircuitBreakerStatus{
					Origin:  "https://upstream.example",
					State:   CircuitBreakerClosed,
					Enabled: true,
				})
			},
			wantTopKeys:       []string{"status", "timestamp", "credentials", "circuit_breaker", "circuit_breakers"},
			wantStatus:        "healthy",
			wantAggregateKeys: aggregateBaseKeys,
			wantAggState:      string(CircuitBreakerClosed),
			wantAggEnabled:    true,
			wantOrigins:       1,
		},
		{
			name: "breaker_half_open",
			setup: func(t *testing.T, s *Server) {
				s.CircuitBreakerStates().Set(CircuitBreakerStatus{
					Origin:              "https://upstream.example",
					State:               CircuitBreakerHalfOpen,
					Enabled:             true,
					ConsecutiveFailures: 2,
				})
			},
			wantTopKeys:       []string{"status", "timestamp", "credentials", "circuit_breaker", "circuit_breakers"},
			wantStatus:        "degraded",
			wantAggregateKeys: aggregateBaseKeys,
			wantAggState:      string(CircuitBreakerHalfOpen),
			wantAggEnabled:    true,
			wantAggFailures:   2,
			wantOrigins:       1,
		},
		{
			name: "breaker_open",
			setup: func(t *testing.T, s *Server) {
				s.CircuitBreakerStates().Set(CircuitBreakerStatus{
					Origin:              "https://upstream.example",
					State:               CircuitBreakerOpen,
					Enabled:             true,
					ConsecutiveFailures: 5,
					OpenedAt:            &openedAt,
					LastError:           "upstream request timed out",
					RetryAfterSeconds:   21,
					Source:              "caller",
				})
			},
			wantTopKeys:       []string{"status", "timestamp", "credentials", "circuit_breaker", "circuit_breakers"},
			wantStatus:        "unhealthy",
			wantAggregateKeys: aggregateOpenKeys,
			wantAggState:      string(CircuitBreakerOpen),
			wantAggEnabled:    true,
			wantAggFailures:   5,
			wantOrigins:       1,
		},
		{
			name: "open_outranks_half_open_regardless_of_order",
			setup: func(t *testing.T, s *Server) {
				// The half-open origin sorts before the open one, so a
				// first-match status walk would stop at degraded. The open
				// record publishes no optional fields, pinning the bare
				// aggregate shape an open state can carry.
				s.CircuitBreakerStates().Set(CircuitBreakerStatus{
					Origin:              "https://a-half-open.example",
					State:               CircuitBreakerHalfOpen,
					Enabled:             true,
					ConsecutiveFailures: 2,
				})
				s.CircuitBreakerStates().Set(CircuitBreakerStatus{
					Origin:              "https://z-open.example",
					State:               CircuitBreakerOpen,
					Enabled:             true,
					ConsecutiveFailures: 7,
				})
			},
			wantTopKeys:       []string{"status", "timestamp", "credentials", "circuit_breaker", "circuit_breakers"},
			wantStatus:        "unhealthy",
			wantAggregateKeys: aggregateBaseKeys,
			wantAggState:      string(CircuitBreakerOpen),
			wantAggEnabled:    true,
			wantAggFailures:   7,
			wantOrigins:       2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newHealthSentinelTestServer(t)
			tt.setup(t, s)

			req := httptest.NewRequest(http.MethodGet, "/health/credentials", nil)
			resp := httptest.NewRecorder()
			s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(resp, req)

			// The verdict lives in the body: an authorized sentinel is a 200
			// in every state, application/json, and never cacheable.
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", resp.Code, resp.Body.String())
			}
			if got := resp.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if got := resp.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if got := resp.Header().Get("X-SEAM-Cache"); got != "" {
				t.Errorf("X-SEAM-Cache = %q, want absent (reserved path bypasses the cache)", got)
			}

			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode body %q: %v", resp.Body.String(), err)
			}

			// The top-level enumeration is closed: exactly the four fixed keys,
			// plus circuit_breakers only when a per-origin record exists.
			if len(body) != len(tt.wantTopKeys) {
				t.Errorf("body carries %d keys (%v), want exactly %v", len(body), body, tt.wantTopKeys)
			}
			for _, key := range tt.wantTopKeys {
				if _, ok := body[key]; !ok {
					t.Errorf("body is missing key %q, got %v", key, body)
				}
			}

			if got, ok := body["status"].(string); !ok || got != tt.wantStatus {
				t.Errorf("status = %v, want %q", body["status"], tt.wantStatus)
			}
			if _, ok := body["timestamp"].(string); !ok {
				t.Errorf("timestamp = %v, want an RFC 3339 string", body["timestamp"])
			}

			// credentials carries availability only; the sentinel does not
			// verify credentials, so it never reports unavailable.
			credentials, ok := body["credentials"].(map[string]any)
			if !ok {
				t.Fatalf("credentials = %v, want an object", body["credentials"])
			}
			if available, ok := credentials["available"].(bool); !ok || !available {
				t.Errorf("credentials.available = %v, want true", credentials["available"])
			}
			if len(credentials) != 1 {
				t.Errorf("credentials carries %d keys (%v), want exactly [available]", len(credentials), credentials)
			}

			aggregate, ok := body["circuit_breaker"].(map[string]any)
			if !ok {
				t.Fatalf("circuit_breaker = %v, want an object", body["circuit_breaker"])
			}
			if len(aggregate) != len(tt.wantAggregateKeys) {
				t.Errorf("circuit_breaker carries %d keys (%v), want exactly %v",
					len(aggregate), aggregate, tt.wantAggregateKeys)
			}
			for _, key := range tt.wantAggregateKeys {
				if _, ok := aggregate[key]; !ok {
					t.Errorf("circuit_breaker is missing key %q, got %v", key, aggregate)
				}
			}
			if got, ok := aggregate["state"].(string); !ok || got != tt.wantAggState {
				t.Errorf("circuit_breaker.state = %v, want %q", aggregate["state"], tt.wantAggState)
			}
			if got, ok := aggregate["enabled"].(bool); !ok || got != tt.wantAggEnabled {
				t.Errorf("circuit_breaker.enabled = %v, want %v", aggregate["enabled"], tt.wantAggEnabled)
			}
			if got, _ := aggregate["consecutive_failures"].(float64); int(got) != tt.wantAggFailures {
				t.Errorf("circuit_breaker.consecutive_failures = %v, want %d", aggregate["consecutive_failures"], tt.wantAggFailures)
			}

			origins, _ := body["circuit_breakers"].([]any)
			if len(origins) != tt.wantOrigins {
				t.Fatalf("circuit_breakers carries %d entries, want %d", len(origins), tt.wantOrigins)
			}
		})
	}
}

// TestCredentialsHealthCallerAccessRejected pins caller-facing rejection of
// the sentinel from both directions a caller can approach it: the caller
// listener, where the path resolves to no route because the spec loader
// reserves it against fragment claims, and the operator listener, where a
// resolved caller without seam:ops:read is default-denied by the scope gate.
// Neither path may echo operator payload.
func TestCredentialsHealthCallerAccessRejected(t *testing.T) {
	t.Run("caller_listener_answers_route_not_found", func(t *testing.T) {
		s := newHealthSentinelTestServer(t)

		resp := httptest.NewRecorder()
		s.identityResolutionMiddleware(s.callerMux).ServeHTTP(resp,
			httptest.NewRequest(http.MethodGet, "/health/credentials", nil))

		if resp.Code != http.StatusNotFound {
			t.Fatalf("caller listener status = %d, want 404 (body: %s)", resp.Code, resp.Body.String())
		}
		var body struct {
			Error   string         `json:"error"`
			Details map[string]any `json:"details"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode caller 404 body %q: %v", resp.Body.String(), err)
		}
		if body.Error != "route_not_found" {
			t.Errorf("caller listener error = %q, want route_not_found", body.Error)
		}
		if got, _ := body.Details["path"].(string); got != "/health/credentials" {
			t.Errorf("caller listener 404 detail path = %q, want /health/credentials", got)
		}
		if str := resp.Body.String(); strings.Contains(str, `"circuit_breaker`) || strings.Contains(str, `"credentials"`) {
			t.Errorf("caller listener 404 echoes operator payload: %s", str)
		}
	})

	t.Run("resolved_caller_without_ops_scope_denied_on_operator_listener", func(t *testing.T) {
		s := newHealthSentinelTestServer(t)
		// A fully-resolved caller identity carrying ordinary proxy scopes but
		// not the operator scope: the scope gate must still deny, and name the
		// missing scope rather than the sentinel payload.
		s.identityResolver.setResolveOverride(func(remoteAddr string) (*Identity, error) {
			return &Identity{
				Resolved:     true,
				NodeName:     "plain-caller",
				NodeKey:      "plain-caller-node-key",
				User:         "caller@example.com",
				Capabilities: []string{"k8s-ro:get"},
			}, nil
		})

		resp := httptest.NewRecorder()
		s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(resp,
			httptest.NewRequest(http.MethodGet, "/health/credentials", nil))

		if resp.Code != http.StatusForbidden {
			t.Fatalf("unscoped caller status = %d, want 403 (body: %s)", resp.Code, resp.Body.String())
		}
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode 403 body %q: %v", resp.Body.String(), err)
		}
		if body.Error != "forbidden" {
			t.Errorf("error = %q, want forbidden", body.Error)
		}
		if !strings.Contains(body.Message, "seam:ops:read") {
			t.Errorf("403 message = %q, want it to name the required scope", body.Message)
		}
		if str := resp.Body.String(); strings.Contains(str, `"circuit_breaker`) || strings.Contains(str, `"credentials"`) {
			t.Errorf("403 echoes operator payload: %s", str)
		}
	})

	t.Run("unresolved_caller_denied_on_operator_listener", func(t *testing.T) {
		// The production WhoIs resolver, not the fixed test identity: an
		// httptest address belongs to no tailnet node, which is exactly the
		// default-deny path an anonymous scrape takes. The shared helper
		// installs a resolving override, so build the server directly.
		s := New(&Config{
			CallerPort:   8080,
			OperatorPort: 8081,
			BaseURL:      "http://localhost:8080",
			SpecDir:      "../../spec",
		})

		resp := httptest.NewRecorder()
		s.identityResolutionMiddleware(s.operatorMux).ServeHTTP(resp,
			httptest.NewRequest(http.MethodGet, "/health/credentials", nil))

		if resp.Code != http.StatusForbidden {
			t.Fatalf("unresolved caller status = %d, want 403 (body: %s)", resp.Code, resp.Body.String())
		}
		// Stage 3 itself denies before the scope gate, so the message names the
		// resolution failure rather than the scope; it must still carry no
		// operator payload.
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode 403 body %q: %v", resp.Body.String(), err)
		}
		if body.Error != "forbidden" {
			t.Errorf("error = %q, want forbidden", body.Error)
		}
		if !strings.Contains(body.Message, "Identity resolution failed") {
			t.Errorf("403 message = %q, want the stage-3 resolution denial", body.Message)
		}
		if str := resp.Body.String(); strings.Contains(str, `"circuit_breaker`) || strings.Contains(str, `"credentials"`) {
			t.Errorf("403 echoes operator payload: %s", str)
		}
	})
}
