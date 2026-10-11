package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

type fakeCredentialSource struct {
	resolved  []byte
	refreshed []byte
	resolve   int
	refresh   int
}

func (s *fakeCredentialSource) Resolve(context.Context, RouteEntry) ([]byte, error) {
	s.resolve++
	return append([]byte(nil), s.resolved...), nil
}

func (s *fakeCredentialSource) Refreshable(context.Context, RouteEntry) ([]byte, error) {
	s.refresh++
	if s.refreshed == nil {
		return nil, errors.New("fake refresh failed")
	}
	return append([]byte(nil), s.refreshed...), nil
}

func TestRouteTableUsesInjectedCredentialSource(t *testing.T) {
	source := &fakeCredentialSource{resolved: []byte("credential-source-test-REPLACE")}
	table := NewRouteTable(nil)
	table.SetCredentialSource(source)

	got, err := table.resolveCredential(context.Background(), RouteEntry{VaultPath: "test/reference"})
	if err != nil {
		t.Fatalf("resolveCredential() error = %v", err)
	}
	if string(got) != "credential-source-test-REPLACE" {
		t.Fatalf("resolveCredential() = %q", got)
	}
	if source.resolve != 1 || source.refresh != 0 {
		t.Fatalf("source calls = resolve:%d refresh:%d, want resolve:1 refresh:0", source.resolve, source.refresh)
	}
}

func TestCredentialSourceRefreshableIsUsedFor401Retry(t *testing.T) {
	source := &fakeCredentialSource{refreshed: []byte("fresh-credential-source-test-REPLACE")}
	req := httptest.NewRequest("GET", "http://upstream.test/resource", nil)
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{VaultPath: "test/reference"}}, source)

	got, err := routeSecretResolverFromRequest(req).Refreshable(context.Background(), RouteEntry{VaultPath: "test/reference"})
	if err != nil {
		t.Fatalf("Refreshable() error = %v", err)
	}
	if string(got) != "fresh-credential-source-test-REPLACE" || source.refresh != 1 {
		t.Fatalf("Refreshable() = %q, calls = %d", got, source.refresh)
	}
}
