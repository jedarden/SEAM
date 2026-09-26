package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// The /docs HTML entry point wires the Agentation feedback toolbar per the
// workspace UI policy. Two independent ways to ship a page that renders
// perfectly while the toolbar silently never mounts are pinned here:
//
//   - the toolbar's module chain imports bare specifiers (react, react-dom,
//     react-dom/client, react/jsx-runtime, agentation) that a browser cannot
//     resolve without an import map, and an import map only applies to module
//     scripts that appear AFTER it in the document; and
//   - agentation is a LIBRARY exporting the <Agentation/> React component —
//     a bare `import "agentation"` resolves, hands back its exports, and
//     renders nothing (verified against agentation@3.1.2 via esm.sh: the
//     module loads, #agentation-root never appears). The page must create
//     #agentation-root and render the component into it.
//
// This test pins the server-side half of that contract over the real route:
// exactly one import map, every bare specifier mapped to the pinned esm.sh
// URL, the map positioned before the module, and the module actually
// mounting the component. The browser-level half — #agentation-root actually
// appearing in the DOM after load — needs a real browser and lives in
// scripts/verify-agentation-mount.sh, which feeds it the page this handler
// renders via the SEAM_AGENTATION_DUMP_HTML hook below.
//
// The expected URLs are spelled out in full (not derived from a shared
// constant with the handler) on purpose: a version bump that edits only one
// side must fail this test, because the map and the module it resolves have
// to move together.

const (
	docsAgentationImportMapTag = `<script type="importmap">`
	docsAgentationModuleTag    = `<script type="module">`
)

// docsAgentationMountSnippets are the load-bearing fragments of the mount
// loader: importing the component, creating the host element, giving it the
// id the UI policy's check looks for, and rendering into it. A module
// missing any one of these leaves #agentation-root absent from the DOM.
var docsAgentationMountSnippets = []string{
	`import { Agentation } from "agentation";`,
	`createElement("div")`,
	`id = "agentation-root";`,
	`createRoot(host).render(`,
}

// docsAgentationExpectedImports is the exact import-map contract: every bare
// specifier the agentation module (and the React peer it renders through)
// imports, resolved to a version-pinned esm.sh URL. agentation declares
// react/react-dom as externals so the map's single React copy is shared —
// without ?external= the esm.sh bundle would bundle its own React instance.
var docsAgentationExpectedImports = map[string]string{
	"react":             "https://esm.sh/react@18.3.1",
	"react-dom":         "https://esm.sh/react-dom@18.3.1",
	"react-dom/client":  "https://esm.sh/react-dom@18.3.1/client",
	"react/jsx-runtime": "https://esm.sh/react@18.3.1/jsx-runtime",
	"agentation":        "https://esm.sh/agentation@3.1.2?external=react,react-dom",
}

func TestDocsAgentationWiring(t *testing.T) {
	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)

	cfg := &Config{
		CallerPort:    callerPort,
		OperatorPort:  operatorPort,
		BaseURL:       fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:       "../../spec",
		AllowlistFile: newBaselineAllowlistFile(t),
	}

	s := New(cfg)
	// /docs is identity-gated; a loopback caller cannot resolve an identity,
	// so present the fixed test identity like the baseline suite does.
	s.identityResolver = newLoopbackTestIdentityResolver()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = s.Shutdown(ctx) }()
	s.setOpenBaoReady(true)

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	url := fmt.Sprintf("http://localhost:%d/docs", callerPort)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	// Ask for the UI representation, not the JSON spec negotiation branch.
	req.Header.Set("Accept", "text/html")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /docs status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET /docs Content-Type = %q, want text/html", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /docs body: %v", err)
	}
	page := string(body)

	// Bridge for scripts/verify-agentation-mount.sh: when the env var is set
	// the rendered page is written there so a real browser can be pointed at
	// exactly these bytes. Unset in every normal run — no filesystem writes.
	if dumpPath := os.Getenv("SEAM_AGENTATION_DUMP_HTML"); dumpPath != "" {
		if err := os.WriteFile(dumpPath, body, 0o600); err != nil {
			t.Fatalf("dump /docs HTML to %s: %v", dumpPath, err)
		}
		t.Logf("dumped /docs HTML to %s", dumpPath)
	}

	// Exactly one import map: a browser honours only the first one in a
	// document, so a second map would silently stop resolving specifiers.
	if got := strings.Count(page, docsAgentationImportMapTag); got != 1 {
		t.Fatalf("import map tags on /docs = %d, want exactly 1", got)
	}

	importMap, mapIdx := extractDocsImportMap(t, page)

	// Every bare specifier the toolbar's module chain imports must be mapped
	// to the pinned URL. A missing key means "Failed to resolve module
	// specifier" in the browser and no toolbar.
	for specifier, wantURL := range docsAgentationExpectedImports {
		gotURL, ok := importMap[specifier]
		if !ok {
			t.Errorf("import map is missing %q (toolbar module would fail to resolve)", specifier)
			continue
		}
		if gotURL != wantURL {
			t.Errorf("import map[%q] = %q, want %q", specifier, gotURL, wantURL)
		}
	}

	// The map must precede the module script — an import map declared after
	// the importing module never applies to it.
	moduleIdx := strings.Index(page, docsAgentationModuleTag)
	if moduleIdx == -1 {
		t.Fatalf("no %q script on /docs — toolbar module is not loaded", docsAgentationModuleTag)
	}
	if mapIdx > moduleIdx {
		t.Errorf("import map (offset %d) appears after the module script (offset %d); a map only applies to scripts after it", mapIdx, moduleIdx)
	}
	// The module script must actually mount the toolbar — importing the
	// library alone renders nothing. Every pinned fragment must live inside
	// that same script: nothing may close the script between the module tag
	// and the fragment.
	moduleEnd := strings.Index(page[moduleIdx:], "</script>")
	if moduleEnd == -1 {
		t.Fatalf("unterminated module script at offset %d", moduleIdx)
	}
	module := page[moduleIdx : moduleIdx+moduleEnd]
	for _, snippet := range docsAgentationMountSnippets {
		if !strings.Contains(module, snippet) {
			t.Errorf("mount loader is missing %q — without it the page renders fine but #agentation-root never appears", snippet)
		}
	}
}

// extractDocsImportMap pulls the JSON body out of the /docs import map
// script tag and returns it parsed plus the tag's offset in the page.
func extractDocsImportMap(t *testing.T, page string) (map[string]string, int) {
	t.Helper()

	start := strings.Index(page, docsAgentationImportMapTag)
	if start == -1 {
		t.Fatalf("no %q on /docs", docsAgentationImportMapTag)
	}
	rest := page[start+len(docsAgentationImportMapTag):]
	end := strings.Index(rest, "</script>")
	if end == -1 {
		t.Fatalf("unterminated import map on /docs")
	}

	var parsed struct {
		Imports map[string]string `json:"imports"`
	}
	if err := json.Unmarshal([]byte(rest[:end]), &parsed); err != nil {
		t.Fatalf("import map is not valid JSON: %v\nmap body:\n%s", err, rest[:end])
	}
	if parsed.Imports == nil {
		t.Fatalf("import map has no \"imports\" object")
	}
	return parsed.Imports, start
}

// TestDocsRouteHTMLRedirectsToAgentationSurface pins the /docs/route half of
// the bead: /docs/route renders no HTML of its own — an Accept: text/html
// request is redirected to /docs#anchor — so the Agentation-wired /docs page
// above is the only feedback surface a browser ever reaches from /docs/route.
// If this branch ever becomes a direct HTML render, that new page needs its
// own import map and mount loader.
func TestDocsRouteHTMLRedirectsToAgentationSurface(t *testing.T) {
	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)

	cfg := &Config{
		CallerPort:    callerPort,
		OperatorPort:  operatorPort,
		BaseURL:       fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:       "../../spec",
		AllowlistFile: newBaselineAllowlistFile(t),
	}

	s := New(cfg)
	s.identityResolver = newLoopbackTestIdentityResolver()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = s.Shutdown(ctx) }()
	s.setOpenBaoReady(true)

	time.Sleep(100 * time.Millisecond)

	url := fmt.Sprintf("http://localhost:%d/docs/route?path=/k8s/get", callerPort)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Accept", "text/html")

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("GET /docs/route (Accept: text/html) status = %d, want %d — the HTML branch must stay a redirect to /docs", resp.StatusCode, http.StatusFound)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/docs#") {
		t.Errorf("redirect Location = %q, want prefix /docs# — /docs/route HTML traffic must land on the Agentation-wired /docs page", loc)
	}
}

// TestDocsPathsNeverRendersHTML pins the third documentation surface: /docs/paths
// is the machine-readable twin of /docs (the Phase 11.2 last-2xx tracker), with
// no Accept negotiation at all — every representation it serves is
// application/json. There is no HTML entry point to wire here, so the toolbar
// contract is satisfied by absence; this test keeps it that way the same way
// TestDocsRouteHTMLRedirectsToAgentationSurface guards the redirect branch. If
// /docs/paths ever grows a text/html representation, that page needs the same
// import map and mount loader the shared shell carries — a page that renders
// fine with no toolbar is exactly the silently-broken state this file exists
// to prevent.
func TestDocsPathsNeverRendersHTML(t *testing.T) {
	callerPort := getAvailablePort(t)
	operatorPort := getAvailablePort(t)

	cfg := &Config{
		CallerPort:    callerPort,
		OperatorPort:  operatorPort,
		BaseURL:       fmt.Sprintf("http://localhost:%d", callerPort),
		SpecDir:       "../../spec",
		AllowlistFile: newBaselineAllowlistFile(t),
	}

	s := New(cfg)
	s.identityResolver = newLoopbackTestIdentityResolver()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = s.Shutdown(ctx) }()
	s.setOpenBaoReady(true)

	time.Sleep(100 * time.Millisecond)

	url := fmt.Sprintf("http://localhost:%d/docs/paths", callerPort)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	// Ask for the UI representation the way a browser would: the pin is that
	// there is none to serve.
	req.Header.Set("Accept", "text/html")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /docs/paths status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("GET /docs/paths Content-Type = %q, want application/json — a text/html branch here would need its own Agentation import map and mount loader", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /docs/paths body: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("/docs/paths body is not valid JSON: %v", err)
	}
	// Belt and suspenders: the machine-readable twin must not smuggle any of
	// the HTML shell's toolbar markers into its body either.
	page := string(body)
	for _, marker := range []string{docsAgentationImportMapTag, docsAgentationModuleTag, "agentation-root"} {
		if strings.Contains(page, marker) {
			t.Errorf("/docs/paths body contains %q — it must stay the machine-readable twin, not grow an unwired HTML branch", marker)
		}
	}
}
