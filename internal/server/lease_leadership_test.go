package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestLeaseLeaderReportsNonNotFoundGetErrors(t *testing.T) {
	var methodsMu sync.Mutex
	var methods []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methodsMu.Lock()
		methods = append(methods, r.Method)
		methodsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"forbidden","reason":"Forbidden","code":403}`))
	}))
	defer api.Close()

	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	leader := &LeaseLeader{
		leaseName:      "seam-credential-probe",
		leaseNamespace: "seam",
		identity:       "test-leader",
		client:         client,
		leaseDuration:  time.Second,
		stopCh:         make(chan struct{}),
	}

	acquired, err := leader.tryAcquireOrRenew(context.Background())
	if acquired {
		t.Fatal("forbidden Lease GET unexpectedly acquired leadership")
	}
	if err == nil || !strings.Contains(err.Error(), "get lease") {
		t.Fatalf("Lease GET error = %v, want wrapped get error", err)
	}
	methodsMu.Lock()
	gotMethods := append([]string(nil), methods...)
	methodsMu.Unlock()
	if len(gotMethods) != 1 || gotMethods[0] != http.MethodGet {
		t.Fatalf("Lease API methods = %v, want only GET after a non-NotFound error", gotMethods)
	}
}
