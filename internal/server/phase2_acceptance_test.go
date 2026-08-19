package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/testutil/openbao"
	"github.com/ardenone/seam/internal/testutil/stubupstream"
)

func TestPhase2Scenario1SecretInjectionAndScrubbingEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	devToken := fmt.Sprintf("phase2-root-%d", time.Now().UnixNano())
	openbaoServer, err := openbao.NewServer(openbao.ServerConfig{
		DevToken:   devToken,
		ListenAddr: reservePhase2TestAddr(t),
	})
	if err != nil {
		t.Skipf("OpenBao dev server unavailable: %v", err)
	}
	defer func() { _ = openbaoServer.Close() }()

	secret := fmt.Sprintf("phase2-acceptance-%d", time.Now().UnixNano())
	vaultPath := "seam/routes/phase2/token"
	if err := openbaoServer.Client().WriteSecret(ctx, vaultPath, map[string]interface{}{"token": secret}); err != nil {
		t.Fatalf("write acceptance secret: %v", err)
	}
	defer func() { _ = openbaoServer.Client().DeleteSecret(ctx, vaultPath) }()

	upstream := stubupstream.New(stubupstream.Config{
		Addr:     reservePhase2TestAddr(t),
		Behavior: stubupstream.BehaviorEcho,
	})
	if err := upstream.Start(); err != nil {
		t.Fatalf("start echo upstream: %v", err)
	}
	defer func() { _ = upstream.Stop(context.Background()) }()
	waitForPhase2HTTP(t, upstream.URL()+"/_control")

	// RouteTable.resolveCredential reads these references at request time; the
	// values remain in the OpenBao process and are not written to the route.
	t.Setenv("SEAM_OPENBAO_ADDR", openbaoServer.BaseURL())
	t.Setenv("SEAM_OPENBAO_DEV_TOKEN", openbaoServer.DevToken())
	t.Setenv("SEAM_OPENBAO_SA_TOKEN_PATH", filepath.Join(t.TempDir(), "no-projected-token"))
	clearPhase2Environment(t, "KUBERNETES_SERVICE_HOST")

	specDir := t.TempDir()
	openapi := fmt.Sprintf(`openapi: 3.1.0
info:
  title: Phase 2 acceptance
  version: 1.0.0
servers: []
paths:
  /phase2:
    get:
      operationId: phase2Acceptance
      x-upstream: %s
      x-vault-path: %s
      x-inject-as:
        kind: header
        name: X-Api-Key
      responses:
        "401":
          description: echoed credential
`, upstream.URL(), vaultPath)
	if err := os.WriteFile(filepath.Join(specDir, "openapi.yaml"), []byte(openapi), 0o600); err != nil {
		t.Fatalf("write acceptance spec: %v", err)
	}
	allowlist := filepath.Join(specDir, "allowlist.yaml")
	if err := os.WriteFile(allowlist, []byte("- localhost\n"), 0o600); err != nil {
		t.Fatalf("write acceptance allowlist: %v", err)
	}

	seamServer := New(&Config{
		CallerPort:    0,
		OperatorPort:  0,
		BaseURL:       "http://localhost",
		SpecDir:       specDir,
		AllowlistFile: allowlist,
	})
	if err := seamServer.Start(ctx); err != nil {
		t.Fatalf("start SEAM: %v", err)
	}
	defer func() { _ = seamServer.Shutdown(context.Background()) }()

	requestURL := fmt.Sprintf("http://127.0.0.1:%d/phase2?keep=1", phase2TestPort(seamServer.callerListener.Addr()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		t.Fatalf("create caller request: %v", err)
	}
	req.Header.Set("X-Api-Key", "caller-supplied")
	req.Header.Set("Authorization", "Bearer caller-supplied")
	req.Header.Set("X-SEAM-Forged", "caller-supplied")
	req.Header.Set("X-SEAM-Dry-Run", "1")
	req.Header.Set("X-SEAM-API-Version", "v1")

	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("call SEAM: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read SEAM response: %v", err)
	}

	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	assertPhase2SecretAbsent(t, body, "response body")
	if !bytes.Contains(body, []byte(RedactedSecret)) {
		t.Fatal("response body did not contain the redaction marker")
	}
	assertPhase2SecretAbsent(t, []byte(response.Header.Get("X-Credential-Echo")), "response header")
	assertPhase2SecretAbsent(t, []byte(response.Trailer.Get("X-Credential-Trailer")), "response trailer")

	calls := upstream.GetCallLog()
	if len(calls) != 1 {
		t.Fatalf("upstream call count = %d, want 1", len(calls))
	}
	call := calls[0]
	if got := call.Headers.Values("X-Api-Key"); len(got) != 1 || got[0] != secret {
		t.Fatalf("upstream did not receive exactly one fetched credential")
	}
	if call.AuthHeader != "" {
		t.Fatal("caller Authorization header survived sanitation")
	}
	if call.Headers.Get("X-Seam-Forged") != "" {
		t.Fatal("forged X-SEAM header survived sanitation")
	}
	if call.Headers.Get("X-Seam-Dry-Run") != "1" || call.Headers.Get("X-Seam-Api-Version") != "v1" {
		t.Fatal("documented X-SEAM exceptions did not survive sanitation")
	}
}

func reservePhase2TestAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("reserve test address: %v", err)
	}
	port := phase2TestPort(listener.Addr())
	_ = listener.Close()
	return fmt.Sprintf("localhost:%d", port)
}

func phase2TestPort(addr net.Addr) int {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		panic("test listener is not TCP")
	}
	return tcpAddr.Port
}

func waitForPhase2HTTP(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(endpoint)
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", endpoint)
}

func assertPhase2SecretAbsent(t *testing.T, data []byte, location string) {
	t.Helper()
	if bytes.Contains(data, []byte("phase2-acceptance-")) {
		t.Fatalf("generated credential leaked in %s", location)
	}
}

func clearPhase2Environment(t *testing.T, name string) {
	t.Helper()
	previous, present := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("clear %s: %v", name, err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(name, previous)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}
