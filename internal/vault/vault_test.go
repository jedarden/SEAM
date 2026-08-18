package vault

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetSecretCachesForTTLAndExpiresWithoutServingStale(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if reads.Load() == 1 {
			_, _ = w.Write([]byte(`{"data":{"data":{"token":"example-old"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"token":"example-new"}}}`))
	}))
	defer server.Close()

	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	client, err := New(Config{
		Address:    server.URL,
		DevToken:   "example-dev-token",
		InCluster:  boolPtr(false),
		HTTPClient: server.Client(),
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := client.GetSecret(context.Background(), "rs-manager/seam/routes/example/token")
	if err != nil {
		t.Fatal(err)
	}
	if got := first["token"]; got != "example-old" {
		t.Fatalf("first value = %v, want example-old", got)
	}
	now = now.Add(29 * time.Second)
	second, err := client.GetSecret(context.Background(), "/rs-manager/seam/routes/example/token")
	if err != nil {
		t.Fatal(err)
	}
	if got := second["token"]; got != "example-old" {
		t.Fatalf("cached value = %v, want example-old", got)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("reads inside TTL = %d, want 1", got)
	}

	now = now.Add(2 * time.Second)
	third, err := client.GetSecret(context.Background(), "rs-manager/seam/routes/example/token")
	if err != nil {
		t.Fatal(err)
	}
	if got := third["token"]; got != "example-new" {
		t.Fatalf("refetched value = %v, want example-new", got)
	}
	if got := reads.Load(); got != 2 {
		t.Fatalf("reads after TTL = %d, want 2", got)
	}
}

func TestOpenBaoOutageDoesNotServeStaleAndHoldsDownPerPath(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if reads.Load() == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"data":{"token":"example-cached"}}}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	client, err := New(Config{
		Address:       server.URL,
		DevToken:      "example-dev-token",
		InCluster:     boolPtr(false),
		HTTPClient:    server.Client(),
		FetchHoldDown: 5 * time.Second,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetSecret(context.Background(), "rs-manager/seam/routes/example/token"); err != nil {
		t.Fatal(err)
	}

	now = now.Add(DefaultCacheTTL + time.Second)
	if value, err := client.GetSecret(context.Background(), "rs-manager/seam/routes/example/token"); value != nil || !IsSecretStoreUnavailable(err) {
		t.Fatalf("expired fetch = value %v, error %v; want no value and unavailable", value, err)
	}
	var unavailable *SecretStoreUnavailableError
	if !errors.As(errFrom(client, now, "rs-manager/seam/routes/example/token"), &unavailable) {
		t.Fatal("expected hold-down error to be typed")
	}
	if got := reads.Load(); got != 2 {
		t.Fatalf("reads after first outage = %d, want 2", got)
	}

	now = now.Add(2 * time.Second)
	if _, err := client.GetSecret(context.Background(), "rs-manager/seam/routes/example/token"); !IsSecretStoreUnavailable(err) {
		t.Fatalf("hold-down error = %v, want unavailable", err)
	}
	if got := reads.Load(); got != 2 {
		t.Fatalf("reads during hold-down = %d, want 2", got)
	}

	now = now.Add(3 * time.Second)
	if _, err := client.GetSecret(context.Background(), "rs-manager/seam/routes/example/token"); !IsSecretStoreUnavailable(err) {
		t.Fatalf("second outage error = %v, want unavailable", err)
	}
	if got := reads.Load(); got != 3 {
		t.Fatalf("reads after hold-down = %d, want 3", got)
	}
}

func TestRefreshAfterUnauthorizedInvalidatesEagerly(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if reads.Load() == 1 {
			_, _ = w.Write([]byte(`{"data":{"data":{"token":"example-old"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"token":"example-new"}}}`))
	}))
	defer server.Close()

	client, err := New(Config{Address: server.URL, DevToken: "example-dev-token", InCluster: boolPtr(false), HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	const vaultPath = "rs-manager/seam/routes/example/token"
	if _, err := client.GetSecret(context.Background(), vaultPath); err != nil {
		t.Fatal(err)
	}
	refreshed, err := client.RefreshAfterUnauthorized(context.Background(), vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshed["token"]; got != "example-new" {
		t.Fatalf("refreshed value = %v, want example-new", got)
	}
	if got := reads.Load(); got != 2 {
		t.Fatalf("reads after eager invalidation = %d, want 2", got)
	}
}

func TestDevTokenIsRefusedInCluster(t *testing.T) {
	_, err := New(Config{Address: "http://example.test", DevToken: "example-dev-token", InCluster: boolPtr(true)})
	if !errors.Is(err, ErrDevTokenInCluster) {
		t.Fatalf("error = %v, want ErrDevTokenInCluster", err)
	}
}

func TestKubernetesAuthUsesProjectedTokenAndReportsMode(t *testing.T) {
	var loginCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/kubernetes/login" {
			loginCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"auth":{"client_token":"example-client-token","lease_duration":60}}`))
			return
		}
		if got := r.Header.Get("X-Vault-Token"); got != "example-client-token" {
			t.Errorf("OpenBao token = %q, want example-client-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{"token":"example"}}}`))
	}))
	defer server.Close()

	client, err := New(Config{
		Address:             server.URL,
		Role:                "example-role",
		InCluster:           boolPtr(true),
		ServiceAccountToken: "example-jwt",
		HTTPClient:          server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetSecret(context.Background(), "rs-manager/seam/routes/example/token"); err != nil {
		t.Fatal(err)
	}
	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("login calls = %d, want 1", got)
	}
	if got := client.AuthMode(); got != "kubernetes" {
		t.Fatalf("auth mode = %q, want kubernetes", got)
	}
}

func TestKubernetesAuthUsesSeparateConfiguredAuthAndKVMounts(t *testing.T) {
	var loginCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/k8s-rs-manager/login" {
			loginCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"auth":{"client_token":"example-client-token","lease_duration":60}}`))
			return
		}
		if r.URL.Path != "/v1/secret/data/rs-manager/seam/routes/example/token" {
			t.Errorf("request path = %q, want separate KV mount path", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{"token":"example"}}}`))
	}))
	defer server.Close()

	client, err := New(Config{
		Address:             server.URL,
		Role:                "example-role",
		MountPath:           "secret",
		AuthMountPath:       "k8s-rs-manager",
		InCluster:           boolPtr(true),
		ServiceAccountToken: "example-jwt",
		HTTPClient:          server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetSecret(context.Background(), "rs-manager/seam/routes/example/token"); err != nil {
		t.Fatal(err)
	}
	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("login calls = %d, want 1", got)
	}
}

func boolPtr(value bool) *bool { return &value }

// errFrom is used only to inspect the typed hold-down error while keeping the
// main outage assertion readable.
func errFrom(client *Client, now time.Time, path string) error {
	_, err := client.GetSecret(context.Background(), path)
	return err
}
