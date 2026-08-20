package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureRedactsCredentialHeadersAndQuery(t *testing.T) {
	const (
		tokenValue    = "test-token-value"
		authorization = "Bearer " + tokenValue
		apiKey        = "test-key"
	)

	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	handler := cm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != authorization {
			t.Errorf("handler Authorization = %q, want %q", got, authorization)
		}
		if got := r.URL.Query().Get("api_key"); got != apiKey {
			t.Errorf("handler api_key = %q, want %q", got, apiKey)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/capture?api_key="+apiKey+"&keep=visible", nil)
	req.Header.Set("Authorization", authorization)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	corpus, raw := saveAndReadRedactionCorpus(t, cm)
	for _, value := range []string{tokenValue, apiKey} {
		if bytes.Contains(raw, []byte(value)) {
			t.Fatalf("saved corpus contains credential value %q", value)
		}
	}

	request := corpus.Entries[0].Request
	if got := request.Headers["Authorization"]; len(got) != 1 || got[0] != RedactedSecret {
		t.Fatalf("captured Authorization = %#v, want redaction marker", got)
	}
	query, err := url.ParseQuery(request.Query)
	if err != nil {
		t.Fatalf("parse captured query: %v", err)
	}
	if got := query.Get("api_key"); got != RedactedSecret {
		t.Fatalf("captured api_key = %q, want %q", got, RedactedSecret)
	}
	if got := query.Get("keep"); got != "visible" {
		t.Fatalf("captured keep = %q, want visible", got)
	}
}

func TestCaptureRedactsRouteInjectableNames(t *testing.T) {
	const (
		headerName  = "X-Partner-Credential"
		headerValue = "route-header-value"
		queryName   = "partner_credential"
		queryValue  = "route-query-value"
	)

	cm := NewCaptureMiddleware(t.TempDir(), "test-service", "test-incumbent", false)
	cm.setRouteTable(&RouteTable{routes: []RouteEntry{
		{
			PathTemplate: "/capture",
			Method:       http.MethodGet,
			APIVersion:   "v1",
			InjectAs:     &InjectAs{Kind: InjectionHeader, Name: headerName},
		},
		{
			PathTemplate: "/capture",
			Method:       http.MethodGet,
			APIVersion:   "v2",
			InjectAs:     &InjectAs{Kind: InjectionQuery, Name: queryName},
		},
	}})
	handler := cm.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(headerName); got != headerValue {
			t.Errorf("handler %s = %q, want %q", headerName, got, headerValue)
		}
		if got := r.URL.Query().Get(queryName); got != queryValue {
			t.Errorf("handler %s = %q, want %q", queryName, got, queryValue)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/capture?"+queryName+"="+queryValue, nil)
	req.Header.Set(headerName, headerValue)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	corpus, raw := saveAndReadRedactionCorpus(t, cm)
	for _, value := range []string{headerValue, queryValue} {
		if bytes.Contains(raw, []byte(value)) {
			t.Fatalf("saved corpus contains route credential value %q", value)
		}
	}

	request := corpus.Entries[0].Request
	if got := request.Headers[headerName]; len(got) != 1 || got[0] != RedactedSecret {
		t.Fatalf("captured %s = %#v, want redaction marker", headerName, got)
	}
	query, err := url.ParseQuery(request.Query)
	if err != nil {
		t.Fatalf("parse captured query: %v", err)
	}
	if got := query.Get(queryName); got != RedactedSecret {
		t.Fatalf("captured %s = %q, want %q", queryName, got, RedactedSecret)
	}
}

func saveAndReadRedactionCorpus(t *testing.T, cm *CaptureMiddleware) (CorpusFile, []byte) {
	t.Helper()

	if err := cm.Save(); err != nil {
		t.Fatalf("save corpus: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(cm.corpusDir, "corpus.json"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var corpus CorpusFile
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("decode corpus: %v", err)
	}
	if len(corpus.Entries) != 1 {
		t.Fatalf("saved corpus entries = %d, want 1", len(corpus.Entries))
	}
	return corpus, raw
}
