package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func jwtWithExp(exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return "h." + payload + ".s"
}

func newOAuthTestServer(t *testing.T, field string, token func() string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("client_id") != "cid" || r.PostForm.Get("refresh_token") != "refresh-1" {
			http.Error(w, "bad grant", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{%q:%q}`, field, token())
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestOAuthRefreshExchangesAndCaches(t *testing.T) {
	tok := jwtWithExp(time.Now().Add(time.Hour))
	srv, calls := newOAuthTestServer(t, "id_token", func() string { return tok })
	inj := &InjectAs{Kind: InjectionOAuthRefresh, TokenURL: srv.URL, ClientID: "cid", TokenField: "id_token"}
	c := newOAuthTokenCache()
	c.client = srv.Client()

	for i := 0; i < 3; i++ {
		got, err := c.bearer(context.Background(), inj, "refresh-1", false)
		if err != nil || got != tok {
			t.Fatalf("bearer() = %q, %v", got, err)
		}
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("token endpoint called %d times, want 1 (cached)", n)
	}
	if _, err := c.bearer(context.Background(), inj, "refresh-1", true); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Fatalf("force did not bypass cache: %d calls", n)
	}
}

func TestOAuthRefreshExpiredTokenReexchanges(t *testing.T) {
	srv, calls := newOAuthTestServer(t, "access_token", func() string { return "opaque" })
	inj := &InjectAs{Kind: InjectionOAuthRefresh, TokenURL: srv.URL, ClientID: "cid"}
	c := newOAuthTokenCache()
	c.client = srv.Client()
	now := time.Now()
	c.now = func() time.Time { return now }

	if _, err := c.bearer(context.Background(), inj, "refresh-1", false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(oauthDefaultLifetime) // past default lifetime minus leeway
	if _, err := c.bearer(context.Background(), inj, "refresh-1", false); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Fatalf("expected re-exchange after expiry, got %d calls", n)
	}
}

func TestOAuthRefreshRotatedRefreshTokenIsNotServedFromCache(t *testing.T) {
	srv, _ := newOAuthTestServer(t, "access_token", func() string { return "opaque" })
	inj := &InjectAs{Kind: InjectionOAuthRefresh, TokenURL: srv.URL, ClientID: "cid"}
	c := newOAuthTokenCache()
	c.client = srv.Client()
	if _, err := c.bearer(context.Background(), inj, "refresh-1", false); err != nil {
		t.Fatal(err)
	}
	// A different (rotated) refresh token is rejected by the server: the cache
	// must not mask that with the old token's bearer.
	if _, err := c.bearer(context.Background(), inj, "refresh-2", false); err == nil {
		t.Fatal("expected error for rejected refresh token")
	}
}

func TestOAuthRefreshErrorsDoNotLeakCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "echo refresh-1 SECRETBODY", http.StatusBadRequest)
	}))
	defer srv.Close()
	inj := &InjectAs{Kind: InjectionOAuthRefresh, TokenURL: srv.URL, ClientID: "cid"}
	c := newOAuthTokenCache()
	c.client = srv.Client()
	_, err := c.bearer(context.Background(), inj, "refresh-1", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "SECRETBODY") || strings.Contains(err.Error(), "refresh-1") {
		t.Fatalf("error leaks upstream body or credential: %v", err)
	}
}

func TestOAuthRefreshMissingFieldFails(t *testing.T) {
	srv, _ := newOAuthTestServer(t, "other", func() string { return "x" })
	inj := &InjectAs{Kind: InjectionOAuthRefresh, TokenURL: srv.URL, ClientID: "cid"}
	c := newOAuthTokenCache()
	c.client = srv.Client()
	if _, err := c.bearer(context.Background(), inj, "refresh-1", false); err == nil {
		t.Fatal("expected error for missing token field")
	}
}

func TestInjectAsOAuthRefreshValidation(t *testing.T) {
	good := &InjectAs{Kind: InjectionOAuthRefresh, TokenURL: "https://login.example/oauth/token", ClientID: "c"}
	if err := good.validate(); err != nil {
		t.Fatalf("valid inject-as rejected: %v", err)
	}
	for name, bad := range map[string]*InjectAs{
		"http url":  {Kind: InjectionOAuthRefresh, TokenURL: "http://x/t", ClientID: "c"},
		"no client": {Kind: InjectionOAuthRefresh, TokenURL: "https://x/t"},
		"has name":  {Kind: InjectionOAuthRefresh, TokenURL: "https://x/t", ClientID: "c", Name: "n"},
	} {
		if err := bad.validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestInjectSecretOAuthRefreshSetsBearer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://u/x", nil)
	req.Header.Set("Authorization", "Bearer caller-supplied")
	inj := &InjectAs{Kind: InjectionOAuthRefresh, TokenURL: "https://x/t", ClientID: "c"}
	if err := InjectSecret(req, inj, []byte("minted")); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer minted" {
		t.Fatalf("Authorization = %q", got)
	}
	_ = url.URL{}
}
