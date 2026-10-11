package server

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
)

func TestEnvCredentialSourceSnapshotsAndTrimsCRLFWithoutLoggingValue(t *testing.T) {
	const (
		name  = "SEAM_CRED_TEST_OWNER_API_KEY"
		value = "fixture-env-credential-value"
	)
	t.Setenv(name, value+"\r\n\n")
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	source := newEnvCredentialSource([]string{name})
	t.Setenv(name, "rotated-after-start")
	got, err := source.Resolve(context.Background(), RouteEntry{CredentialSource: credentialSourceEnv, CredentialEnv: name})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if string(got) != value {
		t.Fatalf("Resolve() = %q, want the process-start value with CR/LF trimmed", got)
	}
	if strings.Contains(logs.String(), value) || strings.Contains(logs.String(), "rotated-after-start") {
		t.Fatalf("credential value appeared in logs: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "bytes") {
		t.Fatalf("expected source log to report only the value length: %q", logs.String())
	}
	if _, err := source.Refreshable(context.Background(), RouteEntry{CredentialSource: credentialSourceEnv, CredentialEnv: name}); err != ErrCredentialSourceNotRefreshable {
		t.Fatalf("Refreshable() error = %v, want ErrCredentialSourceNotRefreshable", err)
	}
}

func TestRouteTableHotReloadReusesProcessCredentialSnapshot(t *testing.T) {
	const (
		name         = "SEAM_CRED_TEST_OWNER_API_KEY"
		initialValue = "credential-at-process-start"
	)
	t.Setenv(name, initialValue)
	document, err := libopenapi.NewDocument([]byte(`{
		"openapi":"3.0.0",
		"info":{"title":"env credential test","version":"1"},
		"x-upstream":"https://api.example.com",
		"x-credential-source":"env",
		"x-credential-env":"SEAM_CRED_TEST_OWNER_API_KEY",
		"x-inject-as":{"kind":"bearer"},
		"paths":{"/test":{"get":{"responses":{"200":{"description":"ok"}}}}}
	}`))
	if err != nil {
		t.Fatalf("NewDocument() error = %v", err)
	}
	model, err := document.BuildV3Model()
	if err != nil {
		t.Fatalf("BuildV3Model() error = %v", err)
	}
	initial, err := BuildRouteTable(&model.Model)
	if err != nil {
		t.Fatalf("BuildRouteTable() error = %v", err)
	}

	t.Setenv(name, "credential-after-start")
	reloaded, err := buildRouteTable(&model.Model, initial.envCredentialSnapshot())
	if err != nil {
		t.Fatalf("hot-reload buildRouteTable() error = %v", err)
	}
	if reloaded.envCredentialSnapshot() != initial.envCredentialSnapshot() {
		t.Fatal("hot reload replaced the process-start credential snapshot")
	}
	secret, err := reloaded.resolveCredential(context.Background(), RouteEntry{
		CredentialSource: credentialSourceEnv,
		CredentialEnv:    name,
	})
	if err != nil {
		t.Fatalf("resolve hot-reloaded credential: %v", err)
	}
	if string(secret) != initialValue {
		t.Fatalf("hot-reloaded credential = %q, want process-start snapshot", secret)
	}
}

func TestEnvCredentialIntegrationScrubsResponsesAndDoesNotRetry401(t *testing.T) {
	const (
		name   = "SEAM_CRED_TEST_OWNER_API_KEY"
		secret = "fixture-env-credential-never-expose"
	)
	t.Setenv(name, secret+"\r\n")
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	source := newEnvCredentialSource([]string{name})
	var echoRequests atomic.Int32
	var probeRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Errorf("upstream Authorization = %q, want injected bearer", got)
		}
		switch r.URL.Path {
		case "/echo":
			echoRequests.Add(1)
			w.Header().Set("X-Echo-Credential", r.Header.Get("Authorization"))
			_, _ = io.WriteString(w, "echo="+secret)
		case "/probe":
			probeRequests.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "revoked="+secret)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxyWithConfig(upstream.URL, &ReverseProxyConfig{MaxBufferedResponseBytes: 1024})
	if err != nil {
		t.Fatalf("NewReverseProxyWithConfig() error = %v", err)
	}
	route := RouteEntry{
		PathTemplate:     "/echo",
		Method:           http.MethodGet,
		APIVersion:       "v1",
		CredentialSource: credentialSourceEnv,
		CredentialEnv:    name,
		InjectAs:         &InjectAs{Kind: InjectionBearer},
	}
	request := httptest.NewRequest(http.MethodGet, "http://seam.test/echo", nil)
	withRouteMatch(request, &RouteMatch{Route: route}, source)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("echo response status = %d, want 200 (body %q)", response.Code, response.Body.String())
	}
	assertNoSecret(t, []byte(secret), response.Body.Bytes())
	assertNoSecret(t, []byte(secret), []byte(response.Header().Get("X-Echo-Credential")))
	if echoRequests.Load() != 1 {
		t.Fatalf("echo upstream requests = %d, want 1", echoRequests.Load())
	}

	// The first 401 is returned to the caller; env values are immutable for
	// this process and are never refreshed or retried in place.
	unauthorizedProxy, err := NewReverseProxyWithConfig(upstream.URL, &ReverseProxyConfig{MaxBufferedResponseBytes: 1024})
	if err != nil {
		t.Fatalf("NewReverseProxyWithConfig() error = %v", err)
	}
	unauthorizedRoute := route
	unauthorizedRoute.PathTemplate = "/probe"
	unauthorized := httptest.NewRequest(http.MethodGet, "http://seam.test/probe", nil)
	withRouteMatch(unauthorized, &RouteMatch{Route: unauthorizedRoute}, source)
	unauthorizedResponse := httptest.NewRecorder()
	unauthorizedProxy.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("env 401 response status = %d, want 401 (body %q)", unauthorizedResponse.Code, unauthorizedResponse.Body.String())
	}
	assertNoSecret(t, []byte(secret), unauthorizedResponse.Body.Bytes())
	if probeRequests.Load() != 1 {
		t.Fatalf("401 upstream requests = %d, want one attempt", probeRequests.Load())
	}

	// Probe the revoked env credential through the same route-table source and
	// verify the operator sentinel reports that probe as unhealthy.
	table := NewRouteTable(nil)
	table.SetCredentialSource(source)
	table.AddRoute(RouteEntry{
		PathTemplate:     "/probe",
		Method:           http.MethodGet,
		APIVersion:       "v1",
		UpstreamTarget:   upstream.URL,
		CredentialSource: credentialSourceEnv,
		CredentialEnv:    name,
		InjectAs:         &InjectAs{Kind: InjectionBearer},
		CredentialProbeConfig: &CredentialProbeConfig{
			Path: "/probe", Method: http.MethodGet, Interval: "1m",
		},
	})
	registry := NewCredentialProbeRegistry()
	loop := &CredentialProbeLoop{
		probeRegistry:    registry,
		routeTableHolder: NewThreadSafeTableHolder(table),
		credentialSource: source,
		httpClient:       upstream.Client(),
		lastProbeTimes:   make(map[string]time.Time),
	}
	loop.probeTarget(context.Background(), &CredentialProbeTarget{
		FragmentID: "probe:get", InstanceID: "_default", CredentialSource: credentialSourceEnv,
		CredentialEnv: name, InjectAs: &InjectAs{Kind: InjectionBearer},
		ProbeURL: upstream.URL + "/probe", ProbePath: "/probe", ProbeMethod: http.MethodGet,
	}, time.Now())
	probe := registry.Get("probe:get", "_default")
	if probe == nil || probe.Status != CredentialUnhealthy || !strings.Contains(probe.LastError, "401") {
		t.Fatalf("env 401 probe result = %+v, want unhealthy status for 401", probe)
	}

	server := &Server{credentialProbes: registry}
	health := httptest.NewRecorder()
	server.credentialsHealthHandler(health, httptest.NewRequest(http.MethodGet, "/health/credentials", nil))
	if !strings.Contains(health.Body.String(), `"status":"unhealthy"`) || !strings.Contains(health.Body.String(), `"credential_probes"`) {
		t.Fatalf("credential health response did not expose the unhealthy env probe: %s", health.Body.String())
	}
	assertNoSecret(t, []byte(secret), health.Body.Bytes())
	assertNoSecret(t, []byte(secret), logs.Bytes())
}
