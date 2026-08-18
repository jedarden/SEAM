package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardenone/seam/internal/spec"
)

func newRequestValidationFixture(t *testing.T) *spec.Loader {
	t.Helper()
	loader, err := spec.New(filepath.Join("testdata", "request-validation"), "http://localhost:8080")
	if err != nil {
		t.Fatalf("load request validation fixture: %v", err)
	}
	return loader
}

func TestRequestValidationFixtureReturnsStructured400(t *testing.T) {
	loader := newRequestValidationFixture(t)
	malformed, err := os.ReadFile(filepath.Join("testdata", "request-validation", "malformed-request.json"))
	if err != nil {
		t.Fatalf("read malformed request fixture: %v", err)
	}

	s := &Server{specLoader: loader}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/widgets/widget-123", bytes.NewReader(malformed))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	s.validationMiddleware(next).ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("malformed request status = %d, want %d", res.Code, http.StatusBadRequest)
	}

	var body SpecValidationResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode structured validation response: %v", err)
	}
	if body.Error != "validation_failed" {
		t.Fatalf("validation error = %q, want validation_failed", body.Error)
	}
	if len(body.ValidationErrors) == 0 {
		t.Fatal("validation response has no field errors")
	}
	failure := body.ValidationErrors[0]
	if !strings.Contains(strings.ToLower(failure.Field), "name") {
		t.Fatalf("field = %q, want it to identify name", failure.Field)
	}
	if failure.ExpectedShape == "" {
		t.Fatal("expected_shape is empty")
	}

	docsURL, err := url.Parse(body.DocsURL)
	if err != nil {
		t.Fatalf("parse docs URL %q: %v", body.DocsURL, err)
	}
	if docsURL.Path != "/docs/route" {
		t.Fatalf("docs path = %q, want /docs/route", docsURL.Path)
	}
	query := docsURL.Query()
	if query.Get("path") != "/widgets/{id}" {
		t.Fatalf("docs path query = %q, want /widgets/{id}", query.Get("path"))
	}
	if query.Get("method") != http.MethodPost || query.Get("version") != "_unversioned" {
		t.Fatalf("docs query = %v, want POST and _unversioned", query)
	}
}

func TestRequestValidationFixtureAllowsValidRequest(t *testing.T) {
	loader := newRequestValidationFixture(t)
	s := &Server{specLoader: loader}
	var forwarded []byte
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		forwarded, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read forwarded request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	body := []byte(`{"name":"Valid widget","count":1}`)
	req := httptest.NewRequest(http.MethodPost, "/widgets/widget-123", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	s.validationMiddleware(next).ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("valid request status = %d, want %d", res.Code, http.StatusNoContent)
	}
	if !bytes.Equal(forwarded, body) {
		t.Fatalf("forwarded body = %s, want %s", forwarded, body)
	}
}

func TestDocsRouteFixtureIncludesRouteSliceAndExample(t *testing.T) {
	s := &Server{specLoader: newRequestValidationFixture(t)}
	path := url.QueryEscape("/widgets/{id}")
	req := httptest.NewRequest(http.MethodGet, "/docs/route?path="+path, nil)
	res := httptest.NewRecorder()
	s.docsRouteHandler(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("docs route status = %d, want %d: %s", res.Code, res.Code, res.Body.String())
	}
	var body map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode route docs: %v", err)
	}
	if body["path"] != "/widgets/{id}" || body["version"] != "_unversioned" {
		t.Fatalf("route identity = path %v, version %v", body["path"], body["version"])
	}
	if body["isDefaultForUnversionedCallers"] != true {
		t.Fatalf("default-version marker = %v, want true", body["isDefaultForUnversionedCallers"])
	}
	if _, ok := body["parameters"].([]interface{}); !ok {
		t.Fatalf("route docs parameters = %T, want array", body["parameters"])
	}

	methods, ok := body["methods"].(map[string]interface{})
	if !ok {
		t.Fatalf("route docs methods = %T, want object", body["methods"])
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if _, ok := methods[method]; !ok {
			t.Fatalf("route docs is missing %s operation", method)
		}
	}
	getOperation := methods[http.MethodGet].(map[string]interface{})
	if getOperation["x-seam-fixture"] != "read-annotation" {
		t.Fatalf("GET annotation = %v", getOperation["x-seam-fixture"])
	}
	postOperation := methods[http.MethodPost].(map[string]interface{})
	if _, ok := postOperation["requestBody"]; !ok {
		t.Fatal("POST operation is missing requestBody schema")
	}
	example, ok := body["example"].(map[string]interface{})
	if !ok {
		t.Fatalf("worked example = %T, want object", body["example"])
	}
	if example["method"] != http.MethodGet || example["path"] != "/widgets/widget-123" {
		t.Fatalf("worked example identity = method %v, path %v", example["method"], example["path"])
	}

	methodReq := httptest.NewRequest(http.MethodGet, "/docs/route?path="+path+"&method=POST&version=_unversioned", nil)
	methodRes := httptest.NewRecorder()
	s.docsRouteHandler(methodRes, methodReq)
	if methodRes.Code != http.StatusOK {
		t.Fatalf("specific docs route status = %d, want %d", methodRes.Code, http.StatusOK)
	}
	var methodBody map[string]interface{}
	if err := json.NewDecoder(methodRes.Body).Decode(&methodBody); err != nil {
		t.Fatalf("decode specific route docs: %v", err)
	}
	operation, ok := methodBody["operation"].(map[string]interface{})
	if !ok {
		t.Fatalf("specific operation = %T, want object", methodBody["operation"])
	}
	if _, ok := operation["responses"]; !ok {
		t.Fatal("specific operation is missing response schemas")
	}
	postExample, ok := methodBody["example"].(map[string]interface{})
	if !ok {
		t.Fatalf("specific worked example = %T, want object", methodBody["example"])
	}
	if _, ok := postExample["body"].(map[string]interface{}); !ok {
		t.Fatalf("specific worked example body = %T, want object", postExample["body"])
	}
}
