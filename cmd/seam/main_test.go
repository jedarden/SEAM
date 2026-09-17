package main

import (
	"flag"
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

// resolveVaultBaseDir is the whole env-var half of the vault base directory
// contract: SEAM_VAULT_BASE_DIR overrides the flag, and when neither names a
// prefix the choice falls to spec.ResolveVaultBaseDir, which is where
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
			name:     "set variable overrides the flag",
			setEnv:   true,
			envValue: "tenants/alpha",
			flag:     "from-flag",
			want:     "tenants/alpha",
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

// serveEnvVarNames is every SEAM_* variable applyEnvOverrides consults except
// SEAM_VAULT_BASE_DIR, which is resolved through the real environment inside
// spec.ResolveVaultBaseDir (pinned by TestResolveVaultBaseDir above).
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
	"SEAM_MAX_REPLAYABLE_REQUEST_BYTES",
	"SEAM_MAX_BUFFERED_RESPONSE_BYTES",
	"SEAM_HOT_RELOAD_ENABLED",
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
	f.applyEnvOverrides(staticGetenv(env))
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

// The serve precedence rule: a set SEAM_* variable beats an explicit flag.
// (SEAM_VAULT_BASE_DIR is excluded — the environment reaches it through
// spec.ResolveVaultBaseDir, not through the injected lookup, and its
// env-over-flag behaviour is asserted by TestResolveVaultBaseDir.)
func TestServeEnvOverridesFlag(t *testing.T) {
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
				if *f.callerPort != 9100 {
					t.Errorf("caller-port = %d, want 9100 (env beats flag)", *f.callerPort)
				}
			},
		},
		{
			name: "operator port",
			args: []string{"--operator-port", "9001"},
			env:  map[string]string{"SEAM_OPERATOR_PORT": "9101"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.operatorPort != 9101 {
					t.Errorf("operator-port = %d, want 9101 (env beats flag)", *f.operatorPort)
				}
			},
		},
		{
			name: "base url",
			args: []string{"--base-url", "http://flag.example"},
			env:  map[string]string{"SEAM_BASE_URL": "http://env.example"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.baseURL != "http://env.example" {
					t.Errorf("base-url = %q, want http://env.example (env beats flag)", *f.baseURL)
				}
			},
		},
		{
			name: "spec dir",
			args: []string{"--spec-dir", "/flag/spec"},
			env:  map[string]string{"SEAM_SPEC_DIR": "/env/spec"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.specDir != "/env/spec" {
					t.Errorf("spec-dir = %q, want /env/spec (env beats flag)", *f.specDir)
				}
			},
		},
		{
			name: "corpus dir",
			args: []string{"--corpus-dir", "flag-corpus"},
			env:  map[string]string{"SEAM_CORPUS_DIR": "env-corpus"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.corpusDir != "env-corpus" {
					t.Errorf("corpus-dir = %q, want env-corpus (env beats flag)", *f.corpusDir)
				}
			},
		},
		{
			name: "fragments dir",
			args: []string{"--fragments-dir", "/flag/fragments"},
			env:  map[string]string{"SEAM_FRAGMENTS_DIR": "/env/fragments"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.fragmentsDir != "/env/fragments" {
					t.Errorf("fragments-dir = %q, want /env/fragments (env beats flag)", *f.fragmentsDir)
				}
			},
		},
		{
			name: "schema path",
			args: []string{"--schema-path", "/flag/schema.json"},
			env:  map[string]string{"SEAM_SCHEMA_PATH": "/env/schema.json"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.schemaPath != "/env/schema.json" {
					t.Errorf("schema-path = %q, want /env/schema.json (env beats flag)", *f.schemaPath)
				}
			},
		},
		{
			name: "upstream ca dir",
			args: []string{"--upstream-ca-dir", "/flag/ca"},
			env:  map[string]string{"SEAM_UPSTREAM_CA_DIR": "/env/ca"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.upstreamCADir != "/env/ca" {
					t.Errorf("upstream-ca-dir = %q, want /env/ca (env beats flag)", *f.upstreamCADir)
				}
			},
		},
		{
			name: "upstream allowlist",
			args: []string{"--allowlist-file", "/flag/allowlist.yaml"},
			env:  map[string]string{"SEAM_UPSTREAM_ALLOWLIST": "/env/allowlist.yaml"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.allowlistFile != "/env/allowlist.yaml" {
					t.Errorf("allowlist-file = %q, want /env/allowlist.yaml (env beats flag)", *f.allowlistFile)
				}
			},
		},
		{
			name: "max replayable request bytes",
			args: []string{"--max-replayable-request-bytes", "12345"},
			env:  map[string]string{"SEAM_MAX_REPLAYABLE_REQUEST_BYTES": "2097152"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.maxReplayableRequestBytes != 2097152 {
					t.Errorf("max-replayable-request-bytes = %d, want 2097152 (env beats flag)", *f.maxReplayableRequestBytes)
				}
			},
		},
		{
			name: "max buffered response bytes",
			args: []string{"--max-buffered-response-bytes", "12345"},
			env:  map[string]string{"SEAM_MAX_BUFFERED_RESPONSE_BYTES": "2097152"},
			check: func(t *testing.T, f *serveFlags) {
				if *f.maxBufferedResponseBytes != 2097152 {
					t.Errorf("max-buffered-response-bytes = %d, want 2097152 (env beats flag)", *f.maxBufferedResponseBytes)
				}
			},
		},
		{
			name: "capture enabled switches on over a flag default",
			env:  map[string]string{"SEAM_CAPTURE_ENABLED": "true"},
			check: func(t *testing.T, f *serveFlags) {
				if !*f.captureEnabled {
					t.Error("capture-enabled = false, want true (env true over default)")
				}
			},
		},
		{
			name: "fragment mode switches on over a flag default",
			env:  map[string]string{"SEAM_FRAGMENT_MODE": "1"},
			check: func(t *testing.T, f *serveFlags) {
				if !*f.fragmentMode {
					t.Error("fragment-mode = false, want true (env 1 over default)")
				}
			},
		},
		{
			name: "hot reload switches on over a flag default",
			env:  map[string]string{"SEAM_HOT_RELOAD_ENABLED": "true"},
			check: func(t *testing.T, f *serveFlags) {
				if !*f.hotReloadEnabled {
					t.Error("enable-hot-reload = false, want true (env true over default)")
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
// is rejected and keeps the flag value. There is no range validation at
// configuration time — an out-of-range port is applied and fails later, when
// the listener binds. The flag is always set to a non-default value so
// "kept" is distinguishable from "defaulted".
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
		{name: "leading whitespace and trailing junk: integer prefix applies", env: " 8080abc", applied: true, value: 8080},
		{name: "hex-looking value takes the decimal prefix", env: "0x10", applied: true, value: 0},
		{name: "non-numeric value is rejected and keeps the flag", env: "abc"},
		{name: "sign-only value is rejected and keeps the flag", env: "-"},
	}

	ports := []struct {
		envVar    string
		flagName  string
		flagValue int
	}{
		{envVar: "SEAM_CALLER_PORT", flagName: "caller-port", flagValue: 9000},
		{envVar: "SEAM_OPERATOR_PORT", flagName: "operator-port", flagValue: 9001},
	}

	for _, port := range ports {
		for _, tc := range tests {
			t.Run(port.envVar+" "+tc.name, func(t *testing.T) {
				args := []string{"--caller-port", "9000", "--operator-port", "9001"}
				f := resolveServeConfig(t, args, map[string]string{port.envVar: tc.env})

				want := tc.value
				if !tc.applied {
					want = port.flagValue
				}
				got := *f.callerPort
				other, otherWant := *f.operatorPort, 9001
				if port.envVar == "SEAM_OPERATOR_PORT" {
					got, other, otherWant = *f.operatorPort, *f.callerPort, 9000
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
// ports, with the flag value kept when no integer prefix is found.
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
		{name: "non-numeric value is rejected and keeps the flag", env: "abc", want: 12345},
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
				f := resolveServeConfig(t,
					[]string{"--" + v.flag, "12345"},
					map[string]string{v.envVar: tc.env})
				if got := *v.field(f); got != tc.want {
					t.Errorf("%s=%q resolved %s = %d, want %d", v.envVar, tc.env, v.flag, got, tc.want)
				}
			})
		}
	}
}

// Boolean variables recognise exactly "true" and "1", lowercase. Every other
// non-empty value — including "TRUE", "yes" and "0" — resolves to false, and
// for fragment-mode and capture-enabled that false wins even over an explicit
// flag. SEAM_HOT_RELOAD_ENABLED is deliberately asymmetric: only "true"/"1"
// changes anything, so the environment can turn hot reload on but never off.
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
				// The flag enables the feature; the variable's false must win.
				args := []string{"--capture-enabled", "--fragment-mode"}
				f := resolveServeConfig(t, args, map[string]string{envVar: value})
				got := *f.captureEnabled
				if envVar == "SEAM_FRAGMENT_MODE" {
					got = *f.fragmentMode
				}
				if got {
					t.Errorf("%s=%q over an enabling flag resolved to true, want false", envVar, value)
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
		flag int
		env  map[string]string
		want int
	}{
		{name: "environment beats the flag", flag: 9000, env: map[string]string{"SEAM_CALLER_PORT": "9100"}, want: 9100},
		{name: "unset environment keeps the flag", flag: 9000, env: nil, want: 9000},
		{name: "empty environment keeps the flag", flag: 9000, env: map[string]string{"SEAM_CALLER_PORT": ""}, want: 9000},
		{name: "non-numeric value keeps the flag", flag: 9000, env: map[string]string{"SEAM_CALLER_PORT": "abc"}, want: 9000},
		{name: "trailing junk takes the integer prefix", flag: 9000, env: map[string]string{"SEAM_CALLER_PORT": "8080abc"}, want: 8080},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveHealthcheckCallerPort(tc.flag, staticGetenv(tc.env)); got != tc.want {
				t.Errorf("resolveHealthcheckCallerPort(%d, %v) = %d, want %d", tc.flag, tc.env, got, tc.want)
			}
		})
	}
}
