package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
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
