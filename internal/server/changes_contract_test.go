package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The /changes version-migration surface is pinned here end to end over the
// real caller pipeline: the success contract (both diff levels), the
// unknown-version semantics, and the reserved-path short-circuit that keeps
// the endpoint outside the spec-validation pipeline. The namespace
// enumeration this pins: /changes is reserved by exact path and has no
// sub-paths — a version-shaped sub-path is an ordinary dispatch lookup and
// 404s through the common error envelope, while an unknown or evicted
// `since` hash is 200 + since_known:false by design, not an error
// (seam-5d1143f9).

const changesContractV1 = `{"paths":{"/v1/widgets":{"get":{"summary":"List widgets"}}}}`

const changesContractV2 = `{"paths":{` +
	`"/v1/widgets":{"get":{"summary":"List widgets (paginated)","responses":{"200":{"description":"OK"}}}},` +
	`"/v2/gadgets":{"get":{"summary":"List gadgets","responses":{"200":{"description":"OK"}}}}}}`

// changesRouteEntry mirrors the served RouteChange schema.
type changesRouteEntry struct {
	Path            string   `json:"path"`
	Verb            string   `json:"verb"`
	ContractKinds   []string `json:"contract_kinds"`
	VisibilityKinds []string `json:"visibility_kinds"`
	DiffURL         string   `json:"diff_url"`
	DocsURL         string   `json:"docs_url"`
	FieldDiff       []struct {
		Field    string `json:"field"`
		OldValue string `json:"old_value"`
		NewValue string `json:"new_value"`
		Change   string `json:"change"`
	} `json:"field_diff"`
}

// changesResponseBody mirrors the served ChangesResponse schema: every field
// the contract marks required.
type changesResponseBody struct {
	SinceSpec      string `json:"since_spec"`
	SinceKnown     bool   `json:"since_known"`
	CurrentSpec    string `json:"current_spec"`
	CurrentVersion string `json:"current_version"`
	Query          struct {
		Level      string `json:"level"`
		Since      string `json:"since"`
		ScopeSince string `json:"scope_since"`
	} `json:"query"`
	Routes       []changesRouteEntry `json:"routes"`
	RouteCount   int                 `json:"route_count"`
	ScopeChanges *struct {
		Scopes     []string `json:"scopes"`
		ChangeType string   `json:"change_type"`
	} `json:"scope_changes"`
}

// seedChangesRingBuffer loads two spec versions, oldest first, so the diff
// has real content: /v1/widgets survives with a changed summary and a newly
// present responses block (response-changed), /v2/gadgets is new (added).
// The buffer is replaced first: New() already seeds it with the repository
// spec, which would otherwise be the diff's oldest version.
func seedChangesRingBuffer(t *testing.T, s *Server) (v1Hash, v2Hash string) {
	t.Helper()
	s.specRingBuffer = NewSpecRingBuffer(10)
	s.specRingBuffer.Add("changescontract-v1hash", "changesv1-0000000000", []byte(changesContractV1), nil)
	s.specRingBuffer.Add("changescontract-v2hash", "changesv2-0000000000", []byte(changesContractV2), nil)
	return "changescontract-v1hash", "changescontract-v2hash"
}

// serveChangesRequest runs one request through the production layering that
// owns the /changes response contract: scope-version outside identity (the
// middleware that stamps X-SEAM-Scope-Version on every caller-port response),
// then stage 3, then the caller mux.
func serveChangesRequest(t *testing.T, s *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	resp := httptest.NewRecorder()
	handler := s.scopeVersionMiddleware(s.identityResolutionMiddleware(s.callerMux))
	handler.ServeHTTP(resp, req)
	return resp
}

func decodeChangesResponse(t *testing.T, resp *httptest.ResponseRecorder) changesResponseBody {
	t.Helper()
	var body changesResponseBody
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /changes response from %q: %v", resp.Body.String(), err)
	}
	return body
}

// TestChangesContractSuccess pins the 200 contract at both diff levels: the
// required top-level fields, the query echo, the header stamp, and the route
// entries with their change kinds and self-describing URLs.
func TestChangesContractSuccess(t *testing.T) {
	s := newControlPlaneContractTestServer(t)
	v1Hash, v2Hash := seedChangesRingBuffer(t, s)

	t.Run("level_1_lists_contract_and_visibility_changes", func(t *testing.T) {
		resp := serveChangesRequest(t, s, http.MethodGet, "/changes")

		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, body: %s", resp.Code, resp.Body.String())
		}
		if ct := resp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if resp.Header().Get("X-SEAM-Scope-Version") == "" {
			t.Error("X-SEAM-Scope-Version header is missing")
		}

		body := decodeChangesResponse(t, resp)
		// No since parameter: the oldest version still in the ring buffer.
		if body.SinceSpec != v1Hash {
			t.Errorf("since_spec = %q, want the oldest version %q", body.SinceSpec, v1Hash)
		}
		if !body.SinceKnown {
			t.Error("since_known = false, want true for a buffered version")
		}
		if body.CurrentSpec != v2Hash {
			t.Errorf("current_spec = %q, want %q", body.CurrentSpec, v2Hash)
		}
		if body.CurrentVersion != "changesv2-0000000000" {
			t.Errorf("current_version = %q, want the truncated version stamp", body.CurrentVersion)
		}
		if body.Query.Level != "1" || body.Query.Since != v1Hash || body.Query.ScopeSince != "" {
			t.Errorf("query echo = %+v, want the defaulted level/since and empty scope-since", body.Query)
		}

		if body.RouteCount != len(body.Routes) {
			t.Errorf("route_count = %d, want len(routes) = %d", body.RouteCount, len(body.Routes))
		}
		if len(body.Routes) != 2 {
			t.Fatalf("routes = %d entries, want 2 (one response-changed, one added)", len(body.Routes))
		}

		added := body.Routes[1]
		if added.Path != "/v2/gadgets" || added.Verb != "get" {
			t.Errorf("routes[1] = %s %s, want get /v2/gadgets (the spec method key is echoed verbatim)", added.Verb, added.Path)
		}
		if len(added.ContractKinds) != 1 || added.ContractKinds[0] != "added" {
			t.Errorf("routes[1] contract_kinds = %v, want [added]", added.ContractKinds)
		}
		if added.DiffURL == "" || added.DocsURL == "" {
			t.Errorf("routes[1] diff_url/docs_url = %q/%q, want both populated", added.DiffURL, added.DocsURL)
		}

		changed := body.Routes[0]
		if changed.Path != "/v1/widgets" || changed.Verb != "get" {
			t.Errorf("routes[0] = %s %s, want get /v1/widgets (the spec method key is echoed verbatim)", changed.Verb, changed.Path)
		}
		if len(changed.ContractKinds) != 1 || changed.ContractKinds[0] != "response-changed" {
			t.Errorf("routes[0] contract_kinds = %v, want [response-changed]", changed.ContractKinds)
		}
	})

	t.Run("level_2_adds_field_diffs", func(t *testing.T) {
		resp := serveChangesRequest(t, s, http.MethodGet, "/changes?level=2")

		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, body: %s", resp.Code, resp.Body.String())
		}
		body := decodeChangesResponse(t, resp)
		if body.Query.Level != "2" {
			t.Fatalf("query.level = %q, want 2", body.Query.Level)
		}

		var changed *changesRouteEntry
		for i := range body.Routes {
			if body.Routes[i].Path == "/v1/widgets" {
				changed = &body.Routes[i]
				break
			}
		}
		if changed == nil {
			t.Fatal("/v1/widgets missing from level 2 routes")
		}

		var summaryChanged, responsesAdded bool
		for _, diff := range changed.FieldDiff {
			switch {
			case diff.Field == "summary" && diff.Change == "changed" &&
				diff.OldValue == "List widgets" && diff.NewValue == "List widgets (paginated)":
				summaryChanged = true
			case diff.Field == "responses" && diff.Change == "added":
				responsesAdded = true
			}
		}
		if !summaryChanged {
			t.Errorf("field_diff = %+v, want the summary change old->new", changed.FieldDiff)
		}
		if !responsesAdded {
			t.Errorf("field_diff = %+v, want the added responses block", changed.FieldDiff)
		}
	})

	t.Run("scope_since_gains_scope_changes_entry", func(t *testing.T) {
		resp := serveChangesRequest(t, s, http.MethodGet, "/changes?scope-since=scopehash-1")

		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, body: %s", resp.Code, resp.Body.String())
		}
		body := decodeChangesResponse(t, resp)
		if body.Query.ScopeSince != "scopehash-1" {
			t.Errorf("query.scope_since = %q, want the supplied hash echoed", body.Query.ScopeSince)
		}
		if body.ScopeChanges == nil {
			t.Fatal("scope_changes missing, want the entry scope-since asks for")
		}
		// Phase 8.4 placeholder: the scope-state diff is not computed yet, so
		// the served change_type is the documented unknown, never a fabricated
		// granted/revoked.
		if body.ScopeChanges.ChangeType != "unknown" {
			t.Errorf("scope_changes.change_type = %q, want the documented unknown placeholder", body.ScopeChanges.ChangeType)
		}
	})
}

// TestChangesContractUnknownSince pins the evicted-since semantics: an
// unknown or evicted hash answers 200 with since_known=false and no route
// entries. This is deliberately NOT a 404 — the contract surface, the
// control-plane notes and the ring buffer all state it is not an error — so
// "fixing" it into the common error envelope would be the regression.
func TestChangesContractUnknownSince(t *testing.T) {
	s := newControlPlaneContractTestServer(t)
	_, v2Hash := seedChangesRingBuffer(t, s)

	resp := serveChangesRequest(t, s, http.MethodGet, "/changes?since=deadbeef00000000")

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an unknown since is not an error), body: %s", resp.Code, resp.Body.String())
	}
	body := decodeChangesResponse(t, resp)
	if body.SinceSpec != "deadbeef00000000" {
		t.Errorf("since_spec = %q, want the requested hash echoed", body.SinceSpec)
	}
	if body.SinceKnown {
		t.Error("since_known = true, want false for a hash outside the ring buffer")
	}
	if len(body.Routes) != 0 || body.RouteCount != 0 {
		t.Errorf("routes/route_count = %d/%d, want 0/0 for an unknown since", len(body.Routes), body.RouteCount)
	}
	if body.CurrentSpec != v2Hash {
		t.Errorf("current_spec = %q, want %q (the diff target is still identified)", body.CurrentSpec, v2Hash)
	}
}

// TestChangesSubPathIsNotAMigrationEndpoint pins the namespace enumeration:
// /changes is reserved by exact path and has no versioned sub-paths. A
// version-shaped sub-path is not a control-plane endpoint — it is not in the
// reserved set, so it falls through to the dispatch catch-all and comes back
// 404 route_not_found in the common error envelope.
func TestChangesSubPathIsNotAMigrationEndpoint(t *testing.T) {
	s := newControlPlaneContractTestServer(t)
	seedChangesRingBuffer(t, s)

	for _, target := range []string{"/changes/1.2.3", "/changes/"} {
		t.Run(target, func(t *testing.T) {
			resp := serveChangesRequest(t, s, http.MethodGet, target)
			assertEnvelope(t, resp, http.StatusNotFound, "route_not_found",
				map[string]string{"method": http.MethodGet, "path": target})
		})
	}
}

// TestChangesContractEmptyRingBuffer pins the no-spec branch: with nothing in
// the ring buffer there is nothing to diff against, and the endpoint says so
// through the common 503 envelope rather than an empty 200.
func TestChangesContractEmptyRingBuffer(t *testing.T) {
	s := newControlPlaneContractTestServer(t)
	s.specRingBuffer = NewSpecRingBuffer(4)

	resp := serveChangesRequest(t, s, http.MethodGet, "/changes")
	assertEnvelope(t, resp, http.StatusServiceUnavailable, "service_unavailable", nil)
}

// TestChangesShortCircuitsSpecValidation pins the reserved-path half of the
// routing contract over the production stage order (validation outside
// identity): no fragment ever declares /changes, so if the endpoint lost its
// reserved-path entry the validator would reject every request before the
// handler ran. A non-reserved unknown path is the control.
func TestChangesShortCircuitsSpecValidation(t *testing.T) {
	s := newControlPlaneContractTestServer(t)
	s.specLoader = newRequestValidationFixture(t)
	s.specRingBuffer = NewSpecRingBuffer(4)

	handler := s.validationMiddleware(s.identityResolutionMiddleware(s.callerMux))

	t.Run("non_reserved_unknown_path_is_validated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/not-a-fragment-route", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assertEnvelope(t, w, http.StatusBadRequest, "validation_failed", nil)
	})

	t.Run("changes_reaches_its_handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/changes", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		// The handler's own envelope (503: empty ring buffer here), not the
		// validator's 400 — proof the request skipped spec validation.
		assertEnvelope(t, w, http.StatusServiceUnavailable, "service_unavailable", nil)
	})
}
