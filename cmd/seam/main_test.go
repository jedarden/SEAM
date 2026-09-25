package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/server"
	"github.com/ardenone/seam/internal/spec"
)

// The container image's HEALTHCHECK depends on these semantics: exit zero only
// when the caller-facing liveness endpoint answers 200.
func TestRunHealthcheck(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr string
	}{
		{name: "serving returns no error", status: http.StatusOK},
		{name: "not ready is an error", status: http.StatusServiceUnavailable, wantErr: "got HTTP 503"},
		{name: "server error is an error", status: http.StatusInternalServerError, wantErr: "got HTTP 500"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/_seam/healthz" {
					t.Errorf("probed %q, want /_seam/healthz", r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			err := runHealthcheck(srv.URL+"/_seam/healthz", 2*time.Second)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// A dead gateway must fail the probe rather than hang or panic.
func TestRunHealthcheckUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL + "/_seam/healthz"
	srv.Close() // nothing is listening now

	if err := runHealthcheck(url, 500*time.Millisecond); err == nil {
		t.Fatal("expected an error probing a closed listener, got nil")
	}
}

func TestAllowlistPathIsFixedInCluster(t *testing.T) {
	if got := resolveAllowlistFile("/tmp/developer-allowlist.yaml", true); got != server.DefaultUpstreamAllowlistFile {
		t.Fatalf("in-cluster allowlist path = %q, want fixed mount %q", got, server.DefaultUpstreamAllowlistFile)
	}
	if got := resolveAllowlistFile("/tmp/developer-allowlist.yaml", false); got != "/tmp/developer-allowlist.yaml" {
		t.Fatalf("local allowlist path = %q, want developer path", got)
	}
}

// resolveUpstreamCADir is the CLI half of the upstream CA trust boundary: the
// bundles that authenticate upstream TLS come from the mounted ConfigMap
// directory inside a cluster, so an operator-supplied --upstream-ca-dir or
// SEAM_UPSTREAM_CA_DIR value cannot replace the mount there, while outside a
// cluster the operator's own directory is accepted.
func TestUpstreamCADirIsFixedInCluster(t *testing.T) {
	const operatorDir = "/tmp/operator-ca-bundles"

	tests := []struct {
		name      string
		requested string
		inCluster bool
		want      string
	}{
		{name: "operator directory refused in-cluster", requested: operatorDir, inCluster: true, want: server.DefaultUpstreamCADir},
		{name: "empty request falls back to the mounted default in-cluster", requested: "", inCluster: true, want: server.DefaultUpstreamCADir},
		{name: "operator directory accepted outside a cluster", requested: operatorDir, inCluster: false, want: operatorDir},
		{name: "empty request falls back to the default outside a cluster", requested: "", inCluster: false, want: server.DefaultUpstreamCADir},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveUpstreamCADir(tc.requested, tc.inCluster); got != tc.want {
				t.Fatalf("resolveUpstreamCADir(%q, %v) = %q, want %q", tc.requested, tc.inCluster, got, tc.want)
			}
		})
	}
}

// detectInClusterEnvironment is the trigger for both trust refusals, and it
// demands both standard Kubernetes service variables: either one alone is an
// accident of the environment, not a cluster.
func TestDetectInClusterEnvironmentRequiresBothServiceVariables(t *testing.T) {
	tests := []struct {
		name          string
		serviceHost   string
		servicePort   string
		wantInCluster bool
	}{
		{name: "both service variables present", serviceHost: "10.96.0.1", servicePort: "443", wantInCluster: true},
		{name: "service host alone is not a cluster", serviceHost: "10.96.0.1", servicePort: "", wantInCluster: false},
		{name: "service port alone is not a cluster", serviceHost: "", servicePort: "443", wantInCluster: false},
		{name: "neither service variable is not a cluster", serviceHost: "", servicePort: "", wantInCluster: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KUBERNETES_SERVICE_HOST", tc.serviceHost)
			t.Setenv("KUBERNETES_PORT", tc.servicePort)

			if got := detectInClusterEnvironment(); got != tc.wantInCluster {
				t.Fatalf("detectInClusterEnvironment() = %v, want %v", got, tc.wantInCluster)
			}
		})
	}
}

// The full refusal chain at the resolution seam: operator-supplied
// SEAM_UPSTREAM_CA_DIR and SEAM_UPSTREAM_ALLOWLIST values reach the flag
// storage through applyEnvOverrides — the path a Deployment actually uses —
// and once the Kubernetes service variables classify the process as
// in-cluster the trust resolvers hand back the mounted paths, while with the
// variables absent the same operator values pass through untouched.
func TestOperatorTrustPathOverridesRespectClusterBoundary(t *testing.T) {
	const (
		operatorCADir     = "/tmp/operator-ca-bundles"
		operatorAllowlist = "/tmp/operator-allowlist.yaml"
	)

	f := resolveServeConfig(t, nil, map[string]string{
		"SEAM_UPSTREAM_CA_DIR":    operatorCADir,
		"SEAM_UPSTREAM_ALLOWLIST": operatorAllowlist,
	})
	if *f.upstreamCADir != operatorCADir || *f.allowlistFile != operatorAllowlist {
		t.Fatalf("env overrides lost: ca-dir=%q allowlist=%q", *f.upstreamCADir, *f.allowlistFile)
	}

	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	t.Setenv("KUBERNETES_PORT", "443")
	if inCluster := detectInClusterEnvironment(); !inCluster {
		t.Fatal("both service variables present, but the environment did not classify as in-cluster")
	} else {
		if got := resolveUpstreamCADir(*f.upstreamCADir, inCluster); got != server.DefaultUpstreamCADir {
			t.Errorf("in-cluster upstream CA dir = %q, want mounted %q", got, server.DefaultUpstreamCADir)
		}
		if got := resolveAllowlistFile(*f.allowlistFile, inCluster); got != server.DefaultUpstreamAllowlistFile {
			t.Errorf("in-cluster allowlist file = %q, want mounted %q", got, server.DefaultUpstreamAllowlistFile)
		}
	}

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_PORT", "")
	if inCluster := detectInClusterEnvironment(); inCluster {
		t.Fatal("cleared service variables classified the environment as in-cluster")
	}
	if got := resolveUpstreamCADir(*f.upstreamCADir, false); got != operatorCADir {
		t.Errorf("local upstream CA dir = %q, want operator value %q", got, operatorCADir)
	}
	if got := resolveAllowlistFile(*f.allowlistFile, false); got != operatorAllowlist {
		t.Errorf("local allowlist file = %q, want operator value %q", got, operatorAllowlist)
	}
}

// applyInClusterTrustBoundary adds the warning half of the refusal: each
// operator-supplied value the boundary drops is announced in the log, so an
// operator can see why their override did not take effect. The matrix pins
// all four cases the upstream-trust contract names — a supplied CA dir and a
// supplied allowlist are refused (and warned) in-cluster, the defaults
// resolve to the mounted paths without any warning, and outside a cluster
// arbitrary paths pass through unchanged and unwarned.
func TestApplyInClusterTrustBoundaryRefusalMatrix(t *testing.T) {
	const (
		operatorCADir     = "/tmp/operator-ca-bundles"
		operatorAllowlist = "/tmp/operator-allowlist.yaml"
	)

	tests := []struct {
		name          string
		inCluster     bool
		caDir         string
		allowlistFile string
		wantCADir     string
		wantAllowlist string
		wantWarnings  []string
	}{
		{
			name:          "in-cluster supplied CA dir is refused and warned",
			inCluster:     true,
			caDir:         operatorCADir,
			wantCADir:     server.DefaultUpstreamCADir,
			wantAllowlist: server.DefaultUpstreamAllowlistFile,
			wantWarnings:  []string{"--upstream-ca-dir"},
		},
		{
			name:          "in-cluster supplied allowlist is ignored and warned",
			inCluster:     true,
			allowlistFile: operatorAllowlist,
			wantCADir:     server.DefaultUpstreamCADir,
			wantAllowlist: server.DefaultUpstreamAllowlistFile,
			wantWarnings:  []string{"--allowlist-file"},
		},
		{
			name:          "in-cluster defaults resolve to the mounted paths without warnings",
			inCluster:     true,
			wantCADir:     server.DefaultUpstreamCADir,
			wantAllowlist: server.DefaultUpstreamAllowlistFile,
		},
		{
			name:          "outside a cluster arbitrary paths pass through without warnings",
			caDir:         operatorCADir,
			allowlistFile: operatorAllowlist,
			wantCADir:     operatorCADir,
			wantAllowlist: operatorAllowlist,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(previous) })

			gotCADir, gotAllowlist := applyInClusterTrustBoundary(tc.caDir, tc.allowlistFile, tc.inCluster)

			if gotCADir != tc.wantCADir {
				t.Errorf("upstream CA dir = %q, want %q", gotCADir, tc.wantCADir)
			}
			if gotAllowlist != tc.wantAllowlist {
				t.Errorf("allowlist file = %q, want %q", gotAllowlist, tc.wantAllowlist)
			}
			for _, warning := range tc.wantWarnings {
				if !strings.Contains(logs.String(), "WARNING: "+warning) {
					t.Errorf("refusal warning naming %q missing from log output %q", warning, logs.String())
				}
			}
			if len(tc.wantWarnings) == 0 && logs.Len() != 0 {
				t.Errorf("expected no warnings, got log output %q", logs.String())
			}
		})
	}
}

// applyInClusterTrustBoundary's warnings are the operator-facing record of a
// refusal, and identifying a refusal must not re-disclose the refused value:
// the WARNING names the flag and the mounted path that replaces it, while the
// operator-supplied value itself — a path chosen outside the cluster,
// potentially pointing into credential-bearing storage — never reaches the
// pod log. This also pins the both-overrides case the refusal matrix above
// does not exercise: two supplied values produce exactly two warnings, one
// per refused option, and both mounted paths win.
func TestApplyInClusterTrustBoundaryWarningsIdentifyWithoutExposing(t *testing.T) {
	const (
		operatorCADir     = "/tmp/operator-secret-ca-bundles"
		operatorAllowlist = "/tmp/operator-secret-allowlist.yaml"
	)

	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	gotCADir, gotAllowlist := applyInClusterTrustBoundary(operatorCADir, operatorAllowlist, true)

	if gotCADir != server.DefaultUpstreamCADir || gotAllowlist != server.DefaultUpstreamAllowlistFile {
		t.Fatalf("both overrides supplied in-cluster: ca-dir=%q allowlist=%q, want the mounted %q and %q",
			gotCADir, gotAllowlist, server.DefaultUpstreamCADir, server.DefaultUpstreamAllowlistFile)
	}

	output := logs.String()
	for _, announcement := range []string{
		"WARNING: --upstream-ca-dir",
		"WARNING: --allowlist-file",
		"using " + server.DefaultUpstreamCADir,
		"using " + server.DefaultUpstreamAllowlistFile,
	} {
		if !strings.Contains(output, announcement) {
			t.Errorf("refusal log %q does not announce %q", output, announcement)
		}
	}
	if warnings := strings.Count(output, "WARNING"); warnings != 2 {
		t.Errorf("both overrides supplied, but the log carries %d WARNING lines, want one per refused value (2): %q", warnings, output)
	}
	for _, refused := range []string{operatorCADir, operatorAllowlist} {
		if strings.Contains(output, refused) {
			t.Errorf("refusal log %q echoes the refused operator value %q", output, refused)
		}
	}
}

// resolveVaultBaseDir is the CLI half of the vault base directory contract:
// the flag wins, then SEAM_VAULT_BASE_DIR, and when neither names a prefix the
// choice falls to spec.DefaultVaultBaseDir, which is where
// spec.DefaultVaultBaseDir lives. Resolving the default here rather than
// handing back "" for server.New to fill in keeps a single authority for the
// prefix, so the binary, the allowlist enforcer and the tests that derive
// fixture paths from the variable all read the same value. The prefix
// ValidateVaultPath enforces is asserted on the server side in
// internal/server.
func TestResolveVaultBaseDir(t *testing.T) {
	const envVar = "SEAM_VAULT_BASE_DIR"
	oldVal, hadOld := os.LookupEnv(envVar)
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(envVar, oldVal)
		} else {
			_ = os.Unsetenv(envVar)
		}
	})

	tests := []struct {
		name     string
		envValue string
		setEnv   bool
		flag     string
		want     string
	}{
		{
			name:   "unset variable leaves the flag value",
			setEnv: false,
			flag:   "tenants/alpha",
			want:   "tenants/alpha",
		},
		{
			// Named for the shared default rather than pinned to a literal: the
			// assertion is that the delegation happened, not which estate the
			// default happens to name today.
			name:   "unset variable and unset flag resolves to the shared default",
			setEnv: false,
			flag:   "",
			want:   spec.DefaultVaultBaseDir,
		},
		{
			name:     "flag overrides set variable",
			setEnv:   true,
			envValue: "tenants/alpha",
			flag:     "from-flag",
			want:     "from-flag",
		},
		{
			name:     "set variable supplies what the flag omits",
			setEnv:   true,
			envValue: "tenants/alpha",
			flag:     "",
			want:     "tenants/alpha",
		},
		{
			name:     "blank variable is treated as unset",
			setEnv:   true,
			envValue: "   ",
			flag:     "from-flag",
			want:     "from-flag",
		},
		{
			name:     "surrounding whitespace is trimmed",
			setEnv:   true,
			envValue: "  tenants/alpha  ",
			flag:     "",
			want:     "tenants/alpha",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				_ = os.Setenv(envVar, tc.envValue)
			} else {
				_ = os.Unsetenv(envVar)
			}

			if got := resolveVaultBaseDir(tc.flag); got != tc.want {
				t.Errorf("resolveVaultBaseDir(%q) with %s=%q = %q, want %q",
					tc.flag, envVar, tc.envValue, got, tc.want)
			}
		})
	}
}

// The tests below pin the CLI/environment configuration precedence contract
// documented in README.md ("Precedence and invalid values"). serveCommand
// itself is unreachable from a test — it starts listeners and exits the
// process — so they drive registerServeFlags + applyEnvOverrides, the same
// wiring serveCommand runs, the way runHealthcheck made the probe testable.

// serveEnvVarNames is every SEAM_* variable applyEnvOverrides consults.
var serveEnvVarNames = []string{
	"SEAM_CALLER_PORT",
	"SEAM_OPERATOR_PORT",
	"SEAM_BASE_URL",
	"SEAM_SPEC_DIR",
	"SEAM_FRAGMENT_MODE",
	"SEAM_SCHEMA_PATH",
	"SEAM_CAPTURE_ENABLED",
	"SEAM_CORPUS_DIR",
	"SEAM_FRAGMENTS_DIR",
	"SEAM_UPSTREAM_CA_DIR",
	"SEAM_UPSTREAM_ALLOWLIST",
	"SEAM_VAULT_BASE_DIR",
	"SEAM_MAX_REPLAYABLE_REQUEST_BYTES",
	"SEAM_MAX_BUFFERED_RESPONSE_BYTES",
	"SEAM_HOT_RELOAD_ENABLED",
}

// serveFlagEnvPairs is the complete flag-to-environment mapping the README's
// environment-variable table documents: one entry per serve flag, paired with
// the SEAM_* variable applyEnvOverrides consults for it. Thirteen pairs follow
// the mechanical rule — SEAM_ plus the flag name upper-cased with dashes as
// underscores — and the two paired by meaning instead are marked derived:
// false. TestServeFlagEnvMapping fails when a serve flag is added without an
// entry here, which is what keeps the README's "all fifteen have a SEAM_*
// environment counterpart" promise true.
var serveFlagEnvPairs = []struct {
	flag    string
	envVar  string
	derived bool
}{
	{"caller-port", "SEAM_CALLER_PORT", true},
	{"operator-port", "SEAM_OPERATOR_PORT", true},
	{"base-url", "SEAM_BASE_URL", true},
	{"spec-dir", "SEAM_SPEC_DIR", true},
	{"fragment-mode", "SEAM_FRAGMENT_MODE", true},
	{"schema-path", "SEAM_SCHEMA_PATH", true},
	{"capture-enabled", "SEAM_CAPTURE_ENABLED", true},
	{"corpus-dir", "SEAM_CORPUS_DIR", true},
	{"fragments-dir", "SEAM_FRAGMENTS_DIR", true},
	{"upstream-ca-dir", "SEAM_UPSTREAM_CA_DIR", true},
	{"allowlist-file", "SEAM_UPSTREAM_ALLOWLIST", false},
	{"vault-base-dir", "SEAM_VAULT_BASE_DIR", true},
	{"max-replayable-request-bytes", "SEAM_MAX_REPLAYABLE_REQUEST_BYTES", true},
	{"max-buffered-response-bytes", "SEAM_MAX_BUFFERED_RESPONSE_BYTES", true},
	{"enable-hot-reload", "SEAM_HOT_RELOAD_ENABLED", false},
}

// clearServeEnv blanks every SEAM_* configuration variable, because an empty
// value is the code's "unset": a variable leaking in from the host
// environment would otherwise silently flip individual cases.
func clearServeEnv(t *testing.T) {
	t.Helper()
	for _, name := range serveEnvVarNames {
		t.Setenv(name, "")
	}
}

// staticGetenv adapts a map to the lookup function applyEnvOverrides takes.
func staticGetenv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// resolveServeConfig runs serveCommand's configuration wiring — register the
// flags, parse args, apply SEAM_* overrides — and hands back the resolved
// values without starting a listener or touching os.Exit.
func resolveServeConfig(t *testing.T, args []string, env map[string]string) *serveFlags {
	t.Helper()
	clearServeEnv(t)
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	f := registerServeFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	f.applyEnvOverrides(staticGetenv(env), fs)
	return f
}

// With no flags and no environment, every knob sits at its documented
// default. SEAM_VAULT_BASE_DIR is blanked by clearServeEnv, so the vault base
// resolves to the shared default spec.DefaultVaultBaseDir.
func TestServeDefaultsWithoutFlagsOrEnv(t *testing.T) {
	f := resolveServeConfig(t, nil, nil)

	if got := *f.callerPort; got != 8080 {
		t.Errorf("caller-port default = %d, want 8080", got)
	}
	if got := *f.operatorPort; got != 8081 {
		t.Errorf("operator-port default = %d, want 8081", got)
	}
	if got := *f.baseURL; got != "http://localhost:8080" {
		t.Errorf("base-url default = %q, want http://localhost:8080", got)
	}
	if got := *f.specDir; got != "./spec" {
		t.Errorf("spec-dir default = %q, want ./spec", got)
	}
	if got := *f.fragmentMode; got {
		t.Error("fragment-mode default = true, want false")
	}
	if got := *f.schemaPath; got != "./spec/route-fragment-schema.json" {
		t.Errorf("schema-path default = %q, want ./spec/route-fragment-schema.json", got)
	}
	if got := *f.captureEnabled; got {
		t.Error("capture-enabled default = true, want false")
	}
	if got := *f.corpusDir; got != "corpus" {
		t.Errorf("corpus-dir default = %q, want corpus", got)
	}
	if got := *f.fragmentsDir; got != "./fragments" {
		t.Errorf("fragments-dir default = %q, want ./fragments", got)
	}
	if got := *f.upstreamCADir; got != "" {
		t.Errorf("upstream-ca-dir default = %q, want empty", got)
	}
	if got := *f.allowlistFile; got != "" {
		t.Errorf("allowlist-file default = %q, want empty", got)
	}
	if got := *f.vaultBaseDir; got != spec.DefaultVaultBaseDir {
		t.Errorf("vault-base-dir default = %q, want spec.DefaultVaultBaseDir (%q)", got, spec.DefaultVaultBaseDir)
	}
	if got := *f.maxReplayableRequestBytes; got != 1024*1024 {
		t.Errorf("max-replayable-request-bytes default = %d, want %d", got, 1024*1024)
	}
	if got := *f.maxBufferedResponseBytes; got != 1024*1024 {
		t.Errorf("max-buffered-response-bytes default = %d, want %d", got, 1024*1024)
	}
	if got := *f.hotReloadEnabled; got {
		t.Error("enable-hot-reload default = true, want false")
	}
}

// The serve precedence rule: an explicit flag beats a set SEAM_* variable.
// Environment values fill flags that were not explicitly passed.
func TestServeFlagOverridesEnv(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		env   map[string]string
		check func(t *testing.T, f *serveFlags)
	}{
		{
			name: "caller port",
			args: []string{"--caller-port", "9000"},
			env:  map[string]string{"SEAM_CALLER_PORT": "9100"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.callerPort != 9000 {
					t.Errorf("caller-port = %d, want 9000 (flag beats env)", *f.callerPort)
				}
			},
		},
		{
			name: "operator port",
			args: []string{"--operator-port", "9001"},
			env:  map[string]string{"SEAM_OPERATOR_PORT": "9101"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.operatorPort != 9001 {
					t.Errorf("operator-port = %d, want 9001 (flag beats env)", *f.operatorPort)
				}
			},
		},
		{
			name: "base url",
			args: []string{"--base-url", "http://flag.example"},
			env:  map[string]string{"SEAM_BASE_URL": "http://env.example"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.baseURL != "http://flag.example" {
					t.Errorf("base-url = %q, want http://flag.example (flag beats env)", *f.baseURL)
				}
			},
		},
		{
			name: "spec dir",
			args: []string{"--spec-dir", "/flag/spec"},
			env:  map[string]string{"SEAM_SPEC_DIR": "/env/spec"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.specDir != "/flag/spec" {
					t.Errorf("spec-dir = %q, want /flag/spec (flag beats env)", *f.specDir)
				}
			},
		},
		{
			name: "corpus dir",
			args: []string{"--corpus-dir", "flag-corpus"},
			env:  map[string]string{"SEAM_CORPUS_DIR": "env-corpus"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.corpusDir != "flag-corpus" {
					t.Errorf("corpus-dir = %q, want flag-corpus (flag beats env)", *f.corpusDir)
				}
			},
		},
		{
			name: "fragments dir",
			args: []string{"--fragments-dir", "/flag/fragments"},
			env:  map[string]string{"SEAM_FRAGMENTS_DIR": "/env/fragments"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.fragmentsDir != "/flag/fragments" {
					t.Errorf("fragments-dir = %q, want /flag/fragments (flag beats env)", *f.fragmentsDir)
				}
			},
		},
		{
			name: "schema path",
			args: []string{"--schema-path", "/flag/schema.json"},
			env:  map[string]string{"SEAM_SCHEMA_PATH": "/env/schema.json"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.schemaPath != "/flag/schema.json" {
					t.Errorf("schema-path = %q, want /flag/schema.json (flag beats env)", *f.schemaPath)
				}
			},
		},
		{
			name: "upstream ca dir",
			args: []string{"--upstream-ca-dir", "/flag/ca"},
			env:  map[string]string{"SEAM_UPSTREAM_CA_DIR": "/env/ca"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.upstreamCADir != "/flag/ca" {
					t.Errorf("upstream-ca-dir = %q, want /flag/ca (flag beats env)", *f.upstreamCADir)
				}
			},
		},
		{
			name: "upstream allowlist",
			args: []string{"--allowlist-file", "/flag/allowlist.yaml"},
			env:  map[string]string{"SEAM_UPSTREAM_ALLOWLIST": "/env/allowlist.yaml"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.allowlistFile != "/flag/allowlist.yaml" {
					t.Errorf("allowlist-file = %q, want /flag/allowlist.yaml (flag beats env)", *f.allowlistFile)
				}
			},
		},
		{
			// The vault branch short-circuits on flagWasSet before the env is
			// consulted, so the flag wins even though the delegated resolver
			// would have preferred it too (TestResolveVaultBaseDir pins that
			// resolver directly; this pins the wiring around it).
			name: "vault base dir",
			args: []string{"--vault-base-dir", "tenants/flag"},
			env:  map[string]string{"SEAM_VAULT_BASE_DIR": "tenants/env"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.vaultBaseDir != "tenants/flag" {
					t.Errorf("vault-base-dir = %q, want tenants/flag (flag beats env)", *f.vaultBaseDir)
				}
			},
		},
		{
			name: "max replayable request bytes",
			args: []string{"--max-replayable-request-bytes", "12345"},
			env:  map[string]string{"SEAM_MAX_REPLAYABLE_REQUEST_BYTES": "2097152"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.maxReplayableRequestBytes != 12345 {
					t.Errorf("max-replayable-request-bytes = %d, want 12345 (flag beats env)", *f.maxReplayableRequestBytes)
				}
			},
		},
		{
			name: "max buffered response bytes",
			args: []string{"--max-buffered-response-bytes", "12345"},
			env:  map[string]string{"SEAM_MAX_BUFFERED_RESPONSE_BYTES": "2097152"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.maxBufferedResponseBytes != 12345 {
					t.Errorf("max-buffered-response-bytes = %d, want 12345 (flag beats env)", *f.maxBufferedResponseBytes)
				}
			},
		},
		{
			name: "capture enabled flag beats false env",
			args: []string{"--capture-enabled"},
			env:  map[string]string{"SEAM_CAPTURE_ENABLED": "false"},
			check: func(t *testing.T, f *serveFlags) {
				if !*f.captureEnabled {
					t.Error("capture-enabled = false, want true (flag beats env)")
				}
			},
		},
		{
			name: "fragment mode flag beats false env",
			args: []string{"--fragment-mode"},
			env:  map[string]string{"SEAM_FRAGMENT_MODE": "false"},
			check: func(t *testing.T, f *serveFlags) {
				if !*f.fragmentMode {
					t.Error("fragment-mode = false, want true (flag beats env)")
				}
			},
		},
		{
			name: "hot reload flag beats false env",
			args: []string{"--enable-hot-reload"},
			env:  map[string]string{"SEAM_HOT_RELOAD_ENABLED": "false"},
			check: func(t *testing.T, f *serveFlags) {
				if !*f.hotReloadEnabled {
					t.Error("enable-hot-reload = false, want true (flag beats env)")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := resolveServeConfig(t, tc.args, tc.env)
			tc.check(t, f)
		})
	}

	// The flag-beats-env direction must stay pinned for every documented
	// pair, the way TestServeEnvFillsOmittedFlag pins env-fills: a mapping
	// entry without a case above fails here, so the table cannot grow an
	// untested pair.
	covered := make(map[string]bool, len(tests))
	for _, tc := range tests {
		if len(tc.env) != 1 {
			t.Fatalf("case %q sets %d environment variables, want exactly 1 — the coverage check keys each case to its paired variable", tc.name, len(tc.env))
		}
		for name := range tc.env {
			covered[name] = true
		}
	}
	for _, name := range serveEnvVarNames {
		if !covered[name] {
			t.Errorf("no flag-beats-env case exists for %s; add one to TestServeFlagOverridesEnv", name)
		}
	}
}

// The mapping must describe the serve flag set exactly — every documented
// flag exists, every flag is documented, the derived pairs really follow the
// mechanical rule, and the only rule-breakers are the two the README names.
// Pinning the count at fifteen is the point: the README promises all fifteen
// serve flags have SEAM_* counterparts, so a sixteenth flag must arrive with
// a mapping entry and a README update or this fails.
func TestServeFlagEnvMapping(t *testing.T) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	registerServeFlags(fs)

	documented := make(map[string]bool, len(serveFlagEnvPairs))
	envSeen := make(map[string]bool, len(serveFlagEnvPairs))
	exceptions := 0
	for _, pair := range serveFlagEnvPairs {
		if documented[pair.flag] {
			t.Errorf("flag --%s is documented twice in serveFlagEnvPairs", pair.flag)
		}
		documented[pair.flag] = true

		if fs.Lookup(pair.flag) == nil {
			t.Errorf("serveFlagEnvPairs pairs --%s with %s, but registerServeFlags defines no --%s flag", pair.flag, pair.envVar, pair.flag)
		}

		if envSeen[pair.envVar] {
			t.Errorf("%s is paired with more than one flag", pair.envVar)
		}
		envSeen[pair.envVar] = true

		if pair.derived {
			want := "SEAM_" + strings.ToUpper(strings.ReplaceAll(pair.flag, "-", "_"))
			if pair.envVar != want {
				t.Errorf("--%s is marked mechanically derived but is paired with %s, want %s", pair.flag, pair.envVar, want)
			}
		} else {
			exceptions++
			// The README pairs these two by meaning, breaking the
			// derivation: the allowlist variable keeps the upstream
			// identity in its name, and the hot-reload variable predates
			// the enable- prefix convention.
			isDocumentedException := (pair.flag == "allowlist-file" && pair.envVar == "SEAM_UPSTREAM_ALLOWLIST") ||
				(pair.flag == "enable-hot-reload" && pair.envVar == "SEAM_HOT_RELOAD_ENABLED")
			if !isDocumentedException {
				t.Errorf("--%s/%s is marked an exception but is not one of the two the README documents", pair.flag, pair.envVar)
			}
		}
	}

	if got := len(serveFlagEnvPairs); got != 15 {
		t.Errorf("serveFlagEnvPairs documents %d flags, want 15 — the README promises all fifteen serve flags have SEAM_* counterparts; update the mapping and the README together", got)
	}
	if exceptions != 2 {
		t.Errorf("%d pairs break the mechanical derivation, want exactly the 2 documented exceptions", exceptions)
	}

	var defined int
	fs.VisitAll(func(*flag.Flag) { defined++ })
	if defined != len(serveFlagEnvPairs) {
		t.Errorf("registerServeFlags defines %d flags but serveFlagEnvPairs documents %d — a serve flag or its SEAM_* counterpart is missing from the mapping", defined, len(serveFlagEnvPairs))
	}

	// clearServeEnv must blank exactly the documented variables, or a host
	// environment value can leak into an individual case through a variable
	// the mapping no longer knows about.
	if len(serveEnvVarNames) != len(serveFlagEnvPairs) {
		t.Errorf("serveEnvVarNames blanks %d variables but the mapping documents %d", len(serveEnvVarNames), len(serveFlagEnvPairs))
	}
	for _, name := range serveEnvVarNames {
		if !envSeen[name] {
			t.Errorf("serveEnvVarNames blanks %s, which the mapping does not document — clearServeEnv and serveFlagEnvPairs disagree", name)
		}
	}
}

// TestServeFlagEnvMapping pins the table to the flag set; this pins the code
// to the table. Every SEAM_* lookup applyEnvOverrides performs must be one of
// the documented variables, and every documented variable must still be
// consulted — so a renamed lookup, a newly added one, or a silently dropped
// one is a configuration-contract change that fails here until the mapping
// and the README's environment-variable table move with it. The lookups run
// against an all-empty environment, where every branch reaches its getenv
// call.
func TestServeEnvOverridesConsultExactlyTheDocumentedVariables(t *testing.T) {
	consulted := make(map[string]bool)
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	f := registerServeFlags(fs)
	f.applyEnvOverrides(func(key string) string {
		consulted[key] = true
		return ""
	}, fs)

	for _, name := range serveEnvVarNames {
		if !consulted[name] {
			t.Errorf("applyEnvOverrides never consulted %s — the lookup was removed or renamed; restore it or move serveEnvVarNames, serveFlagEnvPairs and the README table together", name)
		}
	}

	var undocumented []string
	for key := range consulted {
		documented := false
		for _, name := range serveEnvVarNames {
			if name == key {
				documented = true
				break
			}
		}
		if !documented {
			undocumented = append(undocumented, key)
		}
	}
	if len(undocumented) > 0 {
		sort.Strings(undocumented)
		t.Errorf("applyEnvOverrides consulted variables outside the documented mapping: %v — every SEAM_* lookup is part of the configuration contract; document them or remove the lookups", undocumented)
	}
}

// The other half of the precedence rule: with the flag omitted, a non-empty
// variable supplies the value — for every pair in the mapping, not only the
// ones the flag-beats-env direction already exercises. The fixtures are keyed
// by variable and looked up through serveFlagEnvPairs, so a mapping entry
// without a fixture fails here too and the mapping can never grow an
// untested pair.
func TestServeEnvFillsOmittedFlag(t *testing.T) {
	fixtures := map[string]struct {
		value string
		get   func(f *serveFlags) string
	}{
		"SEAM_CALLER_PORT":                  {value: "9100", get: func(f *serveFlags) string { return fmt.Sprint(*f.callerPort) }},
		"SEAM_OPERATOR_PORT":                {value: "9101", get: func(f *serveFlags) string { return fmt.Sprint(*f.operatorPort) }},
		"SEAM_BASE_URL":                     {value: "http://env.example", get: func(f *serveFlags) string { return *f.baseURL }},
		"SEAM_SPEC_DIR":                     {value: "/env/spec", get: func(f *serveFlags) string { return *f.specDir }},
		"SEAM_FRAGMENT_MODE":                {value: "true", get: func(f *serveFlags) string { return fmt.Sprint(*f.fragmentMode) }},
		"SEAM_SCHEMA_PATH":                  {value: "/env/schema.json", get: func(f *serveFlags) string { return *f.schemaPath }},
		"SEAM_CAPTURE_ENABLED":              {value: "true", get: func(f *serveFlags) string { return fmt.Sprint(*f.captureEnabled) }},
		"SEAM_CORPUS_DIR":                   {value: "env-corpus", get: func(f *serveFlags) string { return *f.corpusDir }},
		"SEAM_FRAGMENTS_DIR":                {value: "/env/fragments", get: func(f *serveFlags) string { return *f.fragmentsDir }},
		"SEAM_UPSTREAM_CA_DIR":              {value: "/env/ca", get: func(f *serveFlags) string { return *f.upstreamCADir }},
		"SEAM_UPSTREAM_ALLOWLIST":           {value: "/env/allowlist.yaml", get: func(f *serveFlags) string { return *f.allowlistFile }},
		"SEAM_VAULT_BASE_DIR":               {value: "tenants/env", get: func(f *serveFlags) string { return *f.vaultBaseDir }},
		"SEAM_MAX_REPLAYABLE_REQUEST_BYTES": {value: "2097152", get: func(f *serveFlags) string { return fmt.Sprint(*f.maxReplayableRequestBytes) }},
		"SEAM_MAX_BUFFERED_RESPONSE_BYTES":  {value: "2097152", get: func(f *serveFlags) string { return fmt.Sprint(*f.maxBufferedResponseBytes) }},
		"SEAM_HOT_RELOAD_ENABLED":           {value: "true", get: func(f *serveFlags) string { return fmt.Sprint(*f.hotReloadEnabled) }},
	}

	for _, pair := range serveFlagEnvPairs {
		fixture, ok := fixtures[pair.envVar]
		if !ok {
			t.Errorf("serveFlagEnvPairs pairs --%s with %s, but no env-fills fixture exists for %s; add one to TestServeEnvFillsOmittedFlag", pair.flag, pair.envVar, pair.envVar)
			continue
		}
		t.Run(pair.envVar, func(t *testing.T) {
			f := resolveServeConfig(t, nil, map[string]string{pair.envVar: fixture.value})
			if got := fixture.get(f); got != fixture.value {
				t.Errorf("%s=%q with --%s omitted resolved %s, want %s", pair.envVar, fixture.value, pair.flag, got, fixture.value)
			}
		})
	}
}

// An empty value is the code's "unset": every consumer guards on val != "",
// so a variable that is set but empty never overrides the flag.
func TestServeEmptyEnvValueKeepsFlag(t *testing.T) {
	f := resolveServeConfig(t,
		[]string{"--caller-port", "9000", "--capture-enabled", "--base-url", "http://flag.example"},
		map[string]string{
			"SEAM_CALLER_PORT":     "",
			"SEAM_CAPTURE_ENABLED": "",
			"SEAM_BASE_URL":        "",
		})

	if *f.callerPort != 9000 {
		t.Errorf("caller-port = %d, want 9000 (empty env counts as unset)", *f.callerPort)
	}
	if !*f.captureEnabled {
		t.Error("capture-enabled = false, want true (empty env counts as unset)")
	}
	if *f.baseURL != "http://flag.example" {
		t.Errorf("base-url = %q, want http://flag.example (empty env counts as unset)", *f.baseURL)
	}
}

// The README's fragment-directory resolution names SEAM_FRAGMENTS_DIR's empty
// value explicitly ("an empty value counts as unset") and gives that variable
// the only documented three-step order: explicit flag, then non-empty
// variable, then the default. The clearServeEnv-based tests already cover the
// empty direction implicitly — every resolveServeConfig call runs with the
// variable empty unless a fixture sets it, so a dropped val != "" guard would
// fail TestServeDefaultsWithoutFlagsOrEnv — but nothing named the case. These
// subtests make the documented behaviour auditable directly; the non-empty
// direction is pinned per-pair in TestServeEnvFillsOmittedFlag.
func TestServeEmptyFragmentsDirEnvCountsAsUnset(t *testing.T) {
	t.Run("empty variable keeps the default", func(t *testing.T) {
		f := resolveServeConfig(t, nil, map[string]string{"SEAM_FRAGMENTS_DIR": ""})
		if got := *f.fragmentsDir; got != "./fragments" {
			t.Errorf("fragments-dir = %q, want the default ./fragments (empty env counts as unset)", got)
		}
	})
	t.Run("empty variable keeps an explicit flag", func(t *testing.T) {
		f := resolveServeConfig(t, []string{"--fragments-dir", "/flag/fragments"}, map[string]string{"SEAM_FRAGMENTS_DIR": ""})
		if got := *f.fragmentsDir; got != "/flag/fragments" {
			t.Errorf("fragments-dir = %q, want /flag/fragments (empty env counts as unset)", got)
		}
	})
}

// README ("Fragment directory and hot-reload scope"): "--spec-dir never
// influences which fragments are loaded", and <spec-dir>/fragments.d survives
// only as a legacy fallback applied when the resolved directory is empty —
// which the serve path reaches solely through an explicitly passed
// --fragments-dir=. These subtests pin both sentences at the serve layer: a
// set SEAM_SPEC_DIR leaves the ./fragments default standing, and an
// explicitly empty --fragments-dir stays empty (the flag was passed, so
// neither the environment nor the default may refill it).
func TestServeSpecDirNeverSelectsFragments(t *testing.T) {
	t.Run("spec-dir env does not redirect fragments resolution", func(t *testing.T) {
		f := resolveServeConfig(t, nil, map[string]string{"SEAM_SPEC_DIR": "/env/spec"})
		if got := *f.fragmentsDir; got != "./fragments" {
			t.Errorf("fragments-dir = %q, want the default ./fragments (SEAM_SPEC_DIR never influences fragment resolution)", got)
		}
	})
	t.Run("explicitly empty --fragments-dir stays empty", func(t *testing.T) {
		f := resolveServeConfig(t, []string{"--fragments-dir="}, map[string]string{"SEAM_FRAGMENTS_DIR": "/env/fragments"})
		if got := *f.fragmentsDir; got != "" {
			t.Errorf("fragments-dir = %q, want %q (an explicitly passed empty flag is the serve path's only route to the legacy fallback)", got, "")
		}
	})
}

// README ("Precedence (serve)") states the empty-value rule for every
// variable, not just the fragments directory: "A variable set to the empty
// string counts as unset". This sweep makes that general statement auditable
// for all fifteen documented pairs, in both directions — with the flag omitted
// the documented default survives, and over an explicit flag the flag value
// survives. The expectations are the documented defaults themselves (the same
// literals TestServeDefaultsWithoutFlagsOrEnv pins) rather than a second
// resolution: through the lookup function applyEnvOverrides takes, an absent
// variable and an empty one are indistinguishable, so comparing two
// resolutions could not tell a dropped val != "" guard from the contract.
func TestServeEmptyEnvValueCountsAsUnsetForEveryPair(t *testing.T) {
	fields := map[string]struct {
		get         func(f *serveFlags) string
		wantDefault string
		args        []string
		wantFlag    string
	}{
		"SEAM_CALLER_PORT": {
			get:         func(f *serveFlags) string { return fmt.Sprint(*f.callerPort) },
			wantDefault: "8080",
			args:        []string{"--caller-port", "9000"},
			wantFlag:    "9000",
		},
		"SEAM_OPERATOR_PORT": {
			get:         func(f *serveFlags) string { return fmt.Sprint(*f.operatorPort) },
			wantDefault: "8081",
			args:        []string{"--operator-port", "9001"},
			wantFlag:    "9001",
		},
		"SEAM_BASE_URL": {
			get:         func(f *serveFlags) string { return *f.baseURL },
			wantDefault: "http://localhost:8080",
			args:        []string{"--base-url", "http://flag.example"},
			wantFlag:    "http://flag.example",
		},
		"SEAM_SPEC_DIR": {
			get:         func(f *serveFlags) string { return *f.specDir },
			wantDefault: "./spec",
			args:        []string{"--spec-dir", "/flag/spec"},
			wantFlag:    "/flag/spec",
		},
		"SEAM_FRAGMENT_MODE": {
			get:         func(f *serveFlags) string { return fmt.Sprint(*f.fragmentMode) },
			wantDefault: "false",
			args:        []string{"--fragment-mode"},
			wantFlag:    "true",
		},
		"SEAM_SCHEMA_PATH": {
			get:         func(f *serveFlags) string { return *f.schemaPath },
			wantDefault: "./spec/route-fragment-schema.json",
			args:        []string{"--schema-path", "/flag/schema.json"},
			wantFlag:    "/flag/schema.json",
		},
		"SEAM_CAPTURE_ENABLED": {
			get:         func(f *serveFlags) string { return fmt.Sprint(*f.captureEnabled) },
			wantDefault: "false",
			args:        []string{"--capture-enabled"},
			wantFlag:    "true",
		},
		"SEAM_CORPUS_DIR": {
			get:         func(f *serveFlags) string { return *f.corpusDir },
			wantDefault: "corpus",
			args:        []string{"--corpus-dir", "flag-corpus"},
			wantFlag:    "flag-corpus",
		},
		"SEAM_FRAGMENTS_DIR": {
			get:         func(f *serveFlags) string { return *f.fragmentsDir },
			wantDefault: "./fragments",
			args:        []string{"--fragments-dir", "/flag/fragments"},
			wantFlag:    "/flag/fragments",
		},
		"SEAM_UPSTREAM_CA_DIR": {
			// The flag-level default is empty here; /etc/gateway/upstream-ca is
			// applied by serveCommand's trust resolver, which has its own
			// in-cluster tests.
			get:         func(f *serveFlags) string { return *f.upstreamCADir },
			wantDefault: "",
			args:        []string{"--upstream-ca-dir", "/flag/ca"},
			wantFlag:    "/flag/ca",
		},
		"SEAM_UPSTREAM_ALLOWLIST": {
			get:         func(f *serveFlags) string { return *f.allowlistFile },
			wantDefault: "",
			args:        []string{"--allowlist-file", "/flag/allowlist.yaml"},
			wantFlag:    "/flag/allowlist.yaml",
		},
		"SEAM_VAULT_BASE_DIR": {
			get:         func(f *serveFlags) string { return *f.vaultBaseDir },
			wantDefault: spec.DefaultVaultBaseDir,
			args:        []string{"--vault-base-dir", "tenants/flag"},
			wantFlag:    "tenants/flag",
		},
		"SEAM_MAX_REPLAYABLE_REQUEST_BYTES": {
			get:         func(f *serveFlags) string { return fmt.Sprint(*f.maxReplayableRequestBytes) },
			wantDefault: "1048576",
			args:        []string{"--max-replayable-request-bytes", "12345"},
			wantFlag:    "12345",
		},
		"SEAM_MAX_BUFFERED_RESPONSE_BYTES": {
			get:         func(f *serveFlags) string { return fmt.Sprint(*f.maxBufferedResponseBytes) },
			wantDefault: "1048576",
			args:        []string{"--max-buffered-response-bytes", "12345"},
			wantFlag:    "12345",
		},
		"SEAM_HOT_RELOAD_ENABLED": {
			get:         func(f *serveFlags) string { return fmt.Sprint(*f.hotReloadEnabled) },
			wantDefault: "false",
			args:        []string{"--enable-hot-reload"},
			wantFlag:    "true",
		},
	}

	for _, pair := range serveFlagEnvPairs {
		field, ok := fields[pair.envVar]
		if !ok {
			t.Errorf("serveFlagEnvPairs pairs --%s with %s, but no empty-value fixture exists for %s; add one to TestServeEmptyEnvValueCountsAsUnsetForEveryPair", pair.flag, pair.envVar, pair.envVar)
			continue
		}
		t.Run(pair.envVar+" empty keeps the default", func(t *testing.T) {
			f := resolveServeConfig(t, nil, map[string]string{pair.envVar: ""})
			if got := field.get(f); got != field.wantDefault {
				t.Errorf("%s=\"\" with --%s omitted resolved %q, want the default %q (empty counts as unset)", pair.envVar, pair.flag, got, field.wantDefault)
			}
		})
		t.Run(pair.envVar+" empty keeps an explicit flag", func(t *testing.T) {
			f := resolveServeConfig(t, field.args, map[string]string{pair.envVar: ""})
			if got := field.get(f); got != field.wantFlag {
				t.Errorf("%s=\"\" over %v resolved %q, want the flag value %q (empty counts as unset)", pair.envVar, field.args, got, field.wantFlag)
			}
		})
	}
}

// Port values parse with fmt.Sscanf %d, whose exact semantics are part of the
// contract: an optional sign and digits with leading whitespace skipped, and
// anything after the integer prefix ignored. A value with no leading integer
// is rejected and keeps the previous value. There is no range validation at
// configuration time — an out-of-range port is applied and fails later, when
// the listener binds. No flag is passed here, so a rejected value is
// distinguishable from the documented default.
func TestServePortEnvParsing(t *testing.T) {
	tests := []struct {
		name string
		env  string
		// applied is false for rejected values, where the flag value is kept.
		applied bool
		value   int
	}{
		{name: "plain value applies", env: "9100", applied: true, value: 9100},
		{name: "signed value applies", env: "-1", applied: true, value: -1},
		{name: "out-of-range value applies unchecked", env: "99999", applied: true, value: 99999},
		{name: "leading whitespace: integer prefix applies", env: " 9100", applied: true, value: 9100},
		{name: "leading whitespace and trailing junk: integer prefix applies", env: " 8080abc", applied: true, value: 8080},
		{name: "hex-looking value takes the decimal prefix", env: "0x10", applied: true, value: 0},
		{name: "non-numeric value is rejected and keeps the flag", env: "abc"},
		{name: "sign-only value is rejected and keeps the flag", env: "-"},
	}

	ports := []struct {
		envVar       string
		flagName     string
		defaultValue int
	}{
		{envVar: "SEAM_CALLER_PORT", flagName: "caller-port", defaultValue: 8080},
		{envVar: "SEAM_OPERATOR_PORT", flagName: "operator-port", defaultValue: 8081},
	}

	for _, port := range ports {
		for _, tc := range tests {
			t.Run(port.envVar+" "+tc.name, func(t *testing.T) {
				f := resolveServeConfig(t, nil, map[string]string{port.envVar: tc.env})

				want := tc.value
				if !tc.applied {
					want = port.defaultValue
				}
				got := *f.callerPort
				other, otherWant := *f.operatorPort, 8081
				if port.envVar == "SEAM_OPERATOR_PORT" {
					got, other, otherWant = *f.operatorPort, *f.callerPort, 8080
				}
				if got != want {
					t.Errorf("%s=%q resolved %s = %d, want %d", port.envVar, tc.env, port.flagName, got, want)
				}
				if other != otherWant {
					t.Errorf("%s leaked into the other port: %d, want %d", port.envVar, other, otherWant)
				}
			})
		}
	}
}

// The byte-limit variables follow the same fmt.Sscanf %d prefix rule as the
// ports, with the default kept when no integer prefix is found.
func TestServeByteLimitEnvParsing(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want int64
	}{
		{name: "plain value applies", env: "2097152", want: 2097152},
		{name: "trailing junk: integer prefix applies", env: "2097152x", want: 2097152},
		{name: "decimal point: integer prefix applies", env: "12.5", want: 12},
		{name: "negative value applies unchecked", env: "-1", want: -1},
		{name: "non-numeric value is rejected and keeps the default", env: "abc", want: 1024 * 1024},
	}

	vars := []struct {
		envVar string
		flag   string
		field  func(f *serveFlags) *int64
	}{
		{"SEAM_MAX_REPLAYABLE_REQUEST_BYTES", "max-replayable-request-bytes", func(f *serveFlags) *int64 { return f.maxReplayableRequestBytes }},
		{"SEAM_MAX_BUFFERED_RESPONSE_BYTES", "max-buffered-response-bytes", func(f *serveFlags) *int64 { return f.maxBufferedResponseBytes }},
	}

	for _, v := range vars {
		for _, tc := range tests {
			t.Run(v.envVar+" "+tc.name, func(t *testing.T) {
				f := resolveServeConfig(t, nil, map[string]string{v.envVar: tc.env})
				if got := *v.field(f); got != tc.want {
					t.Errorf("%s=%q resolved %s = %d, want %d", v.envVar, tc.env, v.flag, got, tc.want)
				}
			})
		}
	}
}

// README ("Invalid values") says a rejected integer variable keeps the
// previous value AND logs a warning. TestServePortEnvParsing and
// TestServeByteLimitEnvParsing pin the kept value; this pins the warning
// itself, on both Printf sites that emit it (ports and byte limits).
func TestServeInvalidIntegerEnvLogsWarning(t *testing.T) {
	tests := []struct {
		name        string
		envVar      string
		env         string
		flagName    string
		field       func(f *serveFlags) int
		keptDefault int
	}{
		{
			name: "SEAM_CALLER_PORT", envVar: "SEAM_CALLER_PORT", env: "abc",
			flagName: "caller-port", field: func(f *serveFlags) int { return *f.callerPort },
			keptDefault: 8080,
		},
		{
			name: "SEAM_OPERATOR_PORT", envVar: "SEAM_OPERATOR_PORT", env: "not-a-port",
			flagName: "operator-port", field: func(f *serveFlags) int { return *f.operatorPort },
			keptDefault: 8081,
		},
		{
			name: "SEAM_MAX_REPLAYABLE_REQUEST_BYTES", envVar: "SEAM_MAX_REPLAYABLE_REQUEST_BYTES", env: "lots",
			flagName: "max-replayable-request-bytes", field: func(f *serveFlags) int { return int(*f.maxReplayableRequestBytes) },
			keptDefault: 1024 * 1024,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log.SetOutput(&buf)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })

			f := resolveServeConfig(t, nil, map[string]string{tc.envVar: tc.env})

			if got := tc.field(f); got != tc.keptDefault {
				t.Errorf("%s=%q resolved %s = %d, want the default %d kept", tc.envVar, tc.env, tc.flagName, got, tc.keptDefault)
			}
			warning := buf.String()
			for _, want := range []string{"invalid", tc.envVar, `"` + tc.env + `"`, fmt.Sprint(tc.keptDefault)} {
				if !strings.Contains(warning, want) {
					t.Errorf("warning %q does not mention %q", warning, want)
				}
			}
		})
	}
}

// Boolean variables recognise exactly "true" and "1", lowercase. Every other
// non-empty value — including "TRUE", "yes" and "0" — resolves to false
// when the environment supplies the value, but an explicit flag still wins.
// SEAM_HOT_RELOAD_ENABLED is deliberately asymmetric: only "true"/"1"
// changes anything when no explicit flag was passed.
func TestServeBooleanEnvForms(t *testing.T) {
	truthy := map[string]bool{"true": true, "1": true}
	falsy := []string{"false", "0", "TRUE", "True", "yes", "on", "banana", " "}

	for _, envVar := range []string{"SEAM_CAPTURE_ENABLED", "SEAM_FRAGMENT_MODE"} {
		for value, want := range truthy {
			t.Run(envVar+"="+value, func(t *testing.T) {
				f := resolveServeConfig(t, nil, map[string]string{envVar: value})
				got := *f.captureEnabled
				if envVar == "SEAM_FRAGMENT_MODE" {
					got = *f.fragmentMode
				}
				if got != want {
					t.Errorf("%s=%q resolved to %v, want %v", envVar, value, got, want)
				}
			})
		}
		for _, value := range falsy {
			t.Run(envVar+"="+value, func(t *testing.T) {
				// The explicit flag enables the feature; the variable's false
				// value must not override it.
				args := []string{"--capture-enabled", "--fragment-mode"}
				f := resolveServeConfig(t, args, map[string]string{envVar: value})
				got := *f.captureEnabled
				if envVar == "SEAM_FRAGMENT_MODE" {
					got = *f.fragmentMode
				}
				if !got {
					t.Errorf("%s=%q over an enabling flag resolved to false, want true", envVar, value)
				}
			})
		}
	}

	hotReloadCases := []struct {
		name    string
		env     string
		args    []string
		want    bool
		because string
	}{
		{name: "true enables", env: "true", args: nil, want: true, because: "true switches hot reload on"},
		{name: "1 enables", env: "1", args: nil, want: true, because: "1 switches hot reload on"},
		{name: "false leaves the default off", env: "false", args: nil, want: false, because: "anything but true/1 is inert"},
		{name: "garbage is inert over the default", env: "banana", args: nil, want: false, because: "anything but true/1 is inert"},
		{name: "false cannot disable an enabling flag", env: "false", args: []string{"--enable-hot-reload"}, want: true, because: "the variable can only turn hot reload on"},
		{name: "garbage cannot disable an enabling flag", env: "banana", args: []string{"--enable-hot-reload"}, want: true, because: "the variable can only turn hot reload on"},
	}
	for _, tc := range hotReloadCases {
		t.Run("SEAM_HOT_RELOAD_ENABLED "+tc.name, func(t *testing.T) {
			f := resolveServeConfig(t, tc.args, map[string]string{"SEAM_HOT_RELOAD_ENABLED": tc.env})
			if *f.hotReloadEnabled != tc.want {
				t.Errorf("SEAM_HOT_RELOAD_ENABLED=%q with args %v resolved enable-hot-reload = %v, want %v (%s)",
					tc.env, tc.args, *f.hotReloadEnabled, tc.want, tc.because)
			}
		})
	}
}

// healthcheck must resolve its port with the same precedence serve does, or a
// Deployment's SEAM_CALLER_PORT override sends the kubelet's probe at the
// wrong listener.
func TestResolveHealthcheckCallerPort(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want int
	}{
		{name: "environment supplies the default", env: map[string]string{"SEAM_CALLER_PORT": "9100"}, want: 9100},
		{name: "explicit flag beats environment", args: []string{"--caller-port", "9000"}, env: map[string]string{"SEAM_CALLER_PORT": "9100"}, want: 9000},
		{name: "unset environment keeps the flag", args: []string{"--caller-port", "9000"}, want: 9000},
		{name: "empty environment keeps the flag", args: []string{"--caller-port", "9000"}, env: map[string]string{"SEAM_CALLER_PORT": ""}, want: 9000},
		{name: "empty environment keeps the default", env: map[string]string{"SEAM_CALLER_PORT": ""}, want: 8080},
		{name: "non-numeric environment keeps the flag", args: []string{"--caller-port", "9000"}, env: map[string]string{"SEAM_CALLER_PORT": "abc"}, want: 9000},
		{name: "non-numeric environment keeps the default", env: map[string]string{"SEAM_CALLER_PORT": "abc"}, want: 8080},
		{name: "leading whitespace: integer prefix applies", env: map[string]string{"SEAM_CALLER_PORT": " 9100"}, want: 9100},
		{name: "trailing junk takes the integer prefix when flag is omitted", env: map[string]string{"SEAM_CALLER_PORT": "8080abc"}, want: 8080},
		{name: "hex-looking value takes the decimal prefix", env: map[string]string{"SEAM_CALLER_PORT": "0x10"}, want: 0},
		{name: "out-of-range value applies unchecked", env: map[string]string{"SEAM_CALLER_PORT": "99999"}, want: 99999},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
			callerPort := fs.Int("caller-port", 8080, "caller port")
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if !flagWasSet(fs, "caller-port") {
				*callerPort = resolveHealthcheckCallerPort(*callerPort, staticGetenv(tc.env))
			}
			if got := *callerPort; got != tc.want {
				t.Errorf("healthcheck args=%v env=%v resolved caller port = %d, want %d", tc.args, tc.env, got, tc.want)
			}
		})
	}
}
