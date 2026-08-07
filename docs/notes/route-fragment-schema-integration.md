# Route Fragment Schema Integration Guide

**Status:** Integration guide for `route-fragment-schema.json` (v1)  
**Created:** 2026-08-07  
**Scope:** Phase 1 (runtime quarantine) and Phase 9 (seam lint)  

## Overview

This guide documents how the SEAM route fragment schema (`docs/notes/route-fragment-schema.json`) is integrated across the SEAM gateway codebase. The schema serves as the single source of truth for fragment validation, used in two critical contexts:

1. **Phase 1b - Runtime Quarantine:** Admission control when fragments are loaded into the gateway
2. **Phase 9a - Seam Lint:** CI-time validation before fragments are committed

**Key principle from ADR-001:** The validator is the product. SEAM's differentiator is schema-validated, structured, field-level error responses. The JSON Schema is portable; the Go validator in `internal/spec` is the production implementation.

---

## Architecture: Two Validation Layers

The schema↔validator boundary is deliberate: JSON Schema validates *shape* and *intra-object* relations, while Go code validates *cross-field*, *cross-path*, *manifest*, and *merge-time* constraints.

```
┌─────────────────────────────────────────────────────────────────┐
│                     Fragment Validation Flow                     │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────────┐        ┌──────────────────┐              │
│  │  JSON Schema     │        │   Go Validator    │              │
│  │  (v1, 2020-12)   │        │   (internal/spec) │              │
│  └────────┬─────────┘        └────────┬─────────┘              │
│           │                           │                         │
│           │ Validates:               │ Validates:             │
│           │ • Field shapes           │ • Cross-path rules     │
│           │ • Required fields        │ • Manifest membership  │
│           │ • Intra-object           │ • Collision keys       │
│           │   relations              │ • Owner co-ownership   │
│           │                           │ • Effective resolution │
│           └───────────┬───────────────┴────────┬──────────────┘
│                       │                               │         │
│                       ▼                               ▼         │
│              ┌────────────────────────────────┐              │
│              │   Validation Result:           │              │
│              │   • Accept                     │              │
│              │   • Reject (schema error)      │              │
│              │   • Quarantine (Go rule)      │              │
│              └────────────────────────────────┘              │
│                       │                                       │
│         ┌─────────────┴─────────────┐                        │
│         ▼                           ▼                         │
│  ┌──────────────┐          ┌──────────────┐                 │
│  │ Phase 9a     │          │ Phase 1b     │                 │
│  │ seam lint    │          │ runtime      │                 │
│  │ (CI gate)    │          │ quarantine   │                 │
│  └──────────────┘          └──────────────┘                 │
└─────────────────────────────────────────────────────────────────┘
```

Both phases use the **same validation engine** in `internal/spec`, ensuring `seam lint` and the gateway never drift apart.

---

## Part 1: Phase 1b - Runtime Quarantine

### 1.1 Admission Control Workflow

When SEAM starts in fragment mode, it loads all fragments from `/etc/gateway/routes.d/<svc>/*.yaml` and validates them before merging into the route table.

**Current implementation** (`internal/spec/loader.go:NewWithFragments`):

```go
func NewWithFragments(specDir, baseURL, schemaPath string) (*Loader, error) {
    // 1. Initialize fragment loader
    fragmentLoader, err := NewFragmentLoader()
    if err != nil {
        return nil, fmt.Errorf("failed to create fragment loader: %w", err)
    }

    // 2. Load fragments from the fragments.d directory
    fragmentsDir := filepath.Join(specDir, "fragments.d")
    if err := fragmentLoader.LoadDirectory(fragmentsDir); err != nil {
        return nil, fmt.Errorf("failed to load fragments: %w", err)
    }

    // 3. Validate fragments against the schema
    if schemaPath != "" {
        if err := fragmentLoader.ValidateFragments(schemaPath); err != nil {
            log.Printf("Warning: fragment validation had errors: %v", err)
        }
    }

    // 4. Merge fragments into a single document
    mergedJSON, err := fragmentLoader.MergeFragments(baseURL)
    if err != nil {
        return nil, fmt.Errorf("failed to merge fragments: %w", err)
    }

    // 5. Build libopenapi model for runtime request validation
    loadedDoc, err := libopenapi.NewDocument(mergedJSON)
    if err != nil {
        return nil, fmt.Errorf("failed to load merged OpenAPI document: %w", err)
    }

    documentModel, err := loadedDoc.BuildV3Model()
    if err != nil {
        return nil, fmt.Errorf("failed to build OpenAPI model: %w", err)
    }

    // 6. Create validator for runtime request validation
    v := validator.NewValidatorFromV3Model(&documentModel.Model, nil)

    return &Loader{
        specPath:       fragmentsDir,
        baseURL:        baseURL,
        rawDocument:    mergedJSON,
        specVersion:    specVersion,
        loadedDoc:      loadedDoc,
        model:          documentModel,
        validator:      v,
        fragmentLoader: fragmentLoader,
        fragmentMode:   true,
    }, nil
}
```

### 1.2 Quarantine Behavior

Fragments that fail validation are **quarantined**, not rejected. The gateway continues with valid fragments; quarantined fragments are excluded from the merge and surfaced at `/config/status`.

**Quarantine reasons** (from `route-fragment-schema.md` §4):

1. **Schema validation failures:** Shape violations caught by JSON Schema
2. **Cross-path violations:** Instance param not in all paths, strip-prefix mismatch
3. **Manifest violations:** Upstream host not in allowlist, vault path outside prefix
4. **Owner violations:** `x-seam-owner` doesn't match mounted directory
5. **Collision violations:** Duplicate `(path, method, x-api-version)` keys

**Status endpoint** (`internal/spec/loader.go:GetFragmentStatus`):

```go
func (l *Loader) GetFragmentStatus() map[string]interface{} {
    if !l.fragmentMode || l.fragmentLoader == nil {
        return map[string]interface{}{
            "fragments_loaded": false,
            "conditions":       []string{},
        }
    }

    conditions := []string{}
    quarantined := l.fragmentLoader.GetQuarantined()

    if len(quarantined) > 0 {
        conditions = append(conditions, fmt.Sprintf("%d fragments quarantined", len(quarantined)))
        for _, q := range quarantined {
            for _, reason := range q.QuarantineReasons {
                conditions = append(conditions, fmt.Sprintf("  - %s: %s", q.SourceFile, reason))
            }
        }
    }

    validCount := l.fragmentLoader.GetValidFragmentCount()
    if validCount == 0 {
        conditions = append(conditions, "No valid fragments loaded")
    } else {
        conditions = append(conditions, fmt.Sprintf("%d valid fragments loaded", validCount))
    }

    return map[string]interface{}{
        "fragments_loaded":   true,
        "valid_count":        validCount,
        "quarantined_count":  len(quarantined),
        "conditions":         conditions,
    }
}
```

### 1.3 Runtime Request Validation

Once fragments are merged and loaded, the gateway validates **incoming HTTP requests** against the merged spec using `pb33f/libopenapi-validator`:

```go
func (l *Loader) ValidateRequest(r *http.Request) *ValidationError {
    // Validate the request
    valid, validationErrors := l.validator.ValidateHttpRequest(r)

    // If valid, request is valid
    if valid {
        return nil
    }

    // Build structured error response
    return &ValidationError{
        Errors: validationErrors,
    }
}
```

**Middleware integration** (`internal/server/validation.go:validationMiddleware`):

```go
func (s *Server) validationMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Skip validation for reserved paths (control plane endpoints)
        if isReservedPath(r.URL.Path) {
            next.ServeHTTP(w, r)
            return
        }

        // Validate the request against the OpenAPI spec
        validationErr := s.specLoader.ValidateRequest(r)
        if validationErr != nil {
            // Request validation failed - return structured 400 error
            errJSON := validationErr.ToJSON(r.URL.Path, r.Method)
            writeValidationError(w, errJSON)
            return
        }

        // Request is valid, proceed to next handler
        next.ServeHTTP(w, r)
    })
}
```

### 1.4 Structured Error Responses

Validation errors follow SEAM's structured 400 format, providing field-level feedback with spec line/column references:

```go
type ValidationErrorResponse struct {
    Error            string                `json:"error"`
    Message          string                `json:"message"`
    ValidationErrors []ValidationFieldError `json:"validation_errors"`
    DocsURL          string               `json:"docs_url"`
}

type ValidationFieldError struct {
    Field          string `json:"field"`
    ExpectedShape  string `json:"expected_shape"`
    Actual         string `json:"actual"`
    Reason         string `json:"reason"`
    Line           int    `json:"line,omitempty"`
    Column         int    `json:"column,omitempty"`
}
```

**Example error response:**

```json
{
  "error": "validation_failed",
  "message": "Request does not conform to the OpenAPI specification",
  "validation_errors": [
    {
      "field": "#/region",
      "expected_shape": "Request validation: type should be string",
      "actual": "123",
      "reason": "Type is invalid",
      "line": 42,
      "column": 8
    }
  ],
  "docs_url": "/docs/route?path=/forecast&method=GET&version=_unversioned"
}
```

---

## Part 2: Phase 9a - Seam Lint

### 2.1 Lint Architecture

`seam lint` is a CI gate that validates fragments **before** they are committed. It uses the **same validation engine** as runtime quarantine, ensuring that what passes lint will pass runtime admission.

**Design principle:** Belt and braces. The gateway never trusts lint; it re-validates at merge time. But lint catches errors in CI, not production.

### 2.2 Lint Implementation (Planned)

The lint command will be implemented in `cmd/seam/main.go`:

```go
var lintCmd = &cobra.Command{
    Use:   "lint <fragment-file>...",
    Short: "Validate SEAM route fragments against the schema",
    Long: `Validate SEAM route fragments against route-fragment-schema.json.
    
Exits with status 1 if any fragment fails validation. Outputs structured
errors indicating which fields violate schema constraints or Go validator rules.`,
    Args:  cobra.MinimumNArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        schemaPath, _ := cmd.Flags().GetString("schema")
        verbose, _ := cmd.Flags().GetBool("verbose")

        // Load the schema
        schema, err := loadSchema(schemaPath)
        if err != nil {
            return fmt.Errorf("failed to load schema: %w", err)
        }

        // Validate each fragment
        allValid := true
        for _, fragmentPath := range args {
            fragment, err := loadFragment(fragmentPath)
            if err != nil {
                fmt.Printf("❌ %s: failed to load: %v\n", fragmentPath, err)
                allValid = false
                continue
            }

            result, err := validateFragment(fragment, schema)
            if err != nil {
                fmt.Printf("❌ %s: validation error: %v\n", fragmentPath, err)
                allValid = false
                continue
            }

            if result.Valid {
                fmt.Printf("✅ %s: valid\n", fragmentPath)
            } else {
                fmt.Printf("❌ %s: invalid\n", fragmentPath)
                allValid = false
                for _, e := range result.Errors {
                    fmt.Printf("   • %s\n", e)
                }
            }

            if verbose {
                fmt.Printf("   Owner: %s\n", result.Owner)
                fmt.Printf("   Paths: %d\n", len(result.Paths))
                fmt.Printf("   Operations: %d\n", result.Operations)
            }
        }

        if !allValid {
            return fmt.Errorf("one or more fragments failed validation")
        }
        return nil
    },
}

func init() {
    rootCmd.AddCommand(lintCmd)
    lintCmd.Flags().String("schema", "docs/notes/route-fragment-schema.json", "Path to schema file")
    lintCmd.Flags().BoolP("verbose", "v", false, "Verbose output")
}
```

### 2.3 Lint Output Format

**Successful lint:**

```
✅ fragments.d/argocd/read-only-proxy.yaml: valid
✅ fragments.d/k8s/fleet-proxies.yaml: valid
```

**Failed lint:**

```
❌ fragments.d/weather/forecast.yaml: invalid
   • Schema violation: x-quota requires x-cost-per-call (constraint-quota-requires-cost)
   • Schema violation: x-upstream-url is not a valid upstreamUrl (IP literal rejected)
   • Validator rule: x-instance-param 'cluster' not found in path /current
   • Manifest rule: upstream host weather.api.example.com not in allowlist
```

### 2.4 CI Integration

**GitHub Actions workflow** (`.github/workflows/seam-lint.yml`):

```yaml
name: SEAM Lint

on:
  push:
    paths:
      - 'fragments.d/**/*.yaml'
  pull_request:

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.22'
          
      - name: Build seam
        run: |
          cd cmd/seam
          go build -o ../../bin/seam
          
      - name: Lint fragments
        run: |
          bin/seam lint fragments.d/**/*.yaml
```

---

## Part 3: Language Choice Implications (ADR-001)

### 3.1 Why Go for the Validator

ADR-001 (2026-07-20) ratified **Go** as SEAM's implementation language. The validator choice was decisive:

**Go is the only ecosystem with:**
- Production-grade, genuine 3.1 request validation (`pb33f/libopenapi-validator`)
- Best-in-class structured error model (JSON pointer + spec line/col + `HowToFix`)
- First-party Tailscale integration (`tsnet`, `client/local`)
- Native OpenBao client

**Alternatives considered and rejected:**
- **Rust:** Would require hand-rolling SEAM's core feature (the validator)
- **Python:** Heaviest footprint; single-maintainer core dependency (`openapi-core`)
- **Node:** "3.1 support" claims inconsistent across libraries; WebSocket proxying "partial"

### 3.2 JSON Schema vs Go Struct Tags

The schema (`route-fragment-schema.json`) is **JSON Schema 2020-12**, not Go structs. This is intentional:

| Aspect | JSON Schema | Go Struct Tags |
|--------|-------------|----------------|
| Portability | ✅ Language-agnostic | ❌ Go-specific |
| Schema evolution | ✅ Versioned (`x-seam-schema: v1`) | ❌ Requires code change |
| Validation engines | ✅ Multiple (Ajv, libopenapi, etc.) | ❌ Go only |
| Tooling ecosystem | ✅ Rich (editors, linters, generators) | ❌ Limited to Go |
| Runtime enforcement | ❌ Requires engine | ✅ Compile-time |

**Decision:** Use JSON Schema as the canonical contract. Go structs are derived from it, not the source of truth.

### 3.3 Current Approach: No Struct Generation

**Current implementation** (`internal/spec/fragment.go`) does **not** generate Go structs from the schema. Instead, it:

1. Loads fragments as raw YAML/JSON
2. Validates against JSON Schema using an external engine (planned: `github.com/santhosh-tekuri/jsonschema` v2, already used by `pb33f/libopenapi`)
3. Accesses fields via map indexing or custom unmarshaling

**Example** (future implementation in `internal/spec/fragment.go`):

```go
package spec

import (
    "os"
    "github.com/santhosh-tekuri/jsonschema/v2"
    _ "embed"
)

//go:embed ../../docs/notes/route-fragment-schema.json
var schemaBytes []byte

var fragmentSchema *jsonschema.Schema

func init() {
    // Compile the schema at package init time
    var err error
    fragmentSchema, err = jsonschema.CompileString("route-fragment-schema.json", string(schemaBytes))
    if err != nil {
        panic(fmt.Errorf("failed to compile fragment schema: %w", err))
    }
}

// ValidateFragment validates a fragment against the schema
func ValidateFragment(fragmentPath string) (*ValidationResult, error) {
    // Load fragment
    f, err := os.ReadFile(fragmentPath)
    if err != nil {
        return nil, fmt.Errorf("failed to read fragment: %w", err)
    }

    // Parse as JSON
    var fragment interface{}
    if err := yaml.Unmarshal(f, &fragment); err != nil {
        return nil, fmt.Errorf("failed to parse fragment YAML: %w", err)
    }

    // Validate against schema
    if err := fragmentSchema.Validate(fragment); err != nil {
        return &ValidationResult{
            Valid:  false,
            Errors: []string{err.(*jsonschema.ValidationError).String()},
        }, nil
    }

    return &ValidationResult{Valid: true}, nil
}
```

### 3.4 Migration Path: If Structs Become Necessary

If Go structs become necessary (e.g., for type-safe fragment manipulation in tooling), the migration path is:

1. **Generate structs from schema** using `github.com/atombender/go-jsonschema`:
   ```bash
   go-jsonschema -o internal/spec/fragment_gen.go \
       docs/notes/route-fragment-schema.json
   ```

2. **Keep JSON Schema as source of truth:** Regenerate structs when schema changes

3. **Add custom Go validation** for §4 rules (cross-field, manifest, merge-time)

4. **Maintain dual validation:** Schema engine for shape, Go code for semantic rules

**Generated struct example** (hypothetical):

```go
// Code generated by go-jsonschema. DO NOT EDIT.

package spec

// UpstreamMap represents the x-upstream-map extension
type UpstreamMap struct {
    AdditionalProperties map[string]UpstreamEntry `json:"-" yaml:"-"`
}

// UpstreamEntry represents a single upstream entry in x-upstream-map
type UpstreamEntry struct {
    // The URL of the upstream service
    Url string `json:"url" yaml:"url"`
    
    // Path to the OpenBao secret containing credentials
    VaultPath string `json:"vaultPath" yaml:"vaultPath"`
    
    // How to inject the credential
    InjectAs *InjectAs `json:"injectAs,omitempty" yaml:"injectAs,omitempty"`
    
    // Required scope for this instance (optional)
    RequiredScope []string `json:"requiredScope,omitempty" yaml:"requiredScope,omitempty"`
    
    // Probe interval for credential validation (optional)
    ProbeInterval string `json:"probeInterval,omitempty" yaml:"probeInterval,omitempty"`
}
```

---

## Part 4: Code Examples

### 4.1 Validating a Fragment (Phase 9a Lint)

```go
package main

import (
    "fmt"
    "os"
    "github.com/jedarden/SEAM/internal/spec"
)

func main() {
    if len(os.Args) < 2 {
        fmt.Println("Usage: seam-lint <fragment.yaml>")
        os.Exit(1)
    }

    fragmentPath := os.Args[1]
    
    // Load the schema (embedded at compile time)
    schemaPath := "docs/notes/route-fragment-schema.json"
    
    // Validate the fragment
    result, err := spec.ValidateFragment(fragmentPath, schemaPath)
    if err != nil {
        fmt.Printf("❌ Validation failed: %v\n", err)
        os.Exit(1)
    }
    
    if result.Valid {
        fmt.Printf("✅ %s: valid\n", fragmentPath)
        os.Exit(0)
    }
    
    // Print errors
    fmt.Printf("❌ %s: invalid\n", fragmentPath)
    for _, e := range result.Errors {
        fmt.Printf("   • %s\n", e)
    }
    os.Exit(1)
}
```

### 4.2 Loading Fragments at Runtime (Phase 1b)

```go
package spec

import (
    "fmt"
    "path/filepath"
)

// FragmentLoader handles loading and validating route fragments
type FragmentLoader struct {
    fragments    []*Fragment
    quarantined  []*QuarantinedFragment
    schema       *jsonschema.Schema
}

// LoadDirectory loads all fragments from a directory
func (fl *FragmentLoader) LoadDirectory(dir string) error {
    files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
    if err != nil {
        return fmt.Errorf("failed to glob fragments: %w", err)
    }

    for _, file := range files {
        fragment, err := LoadFragment(file)
        if err != nil {
            return fmt.Errorf("failed to load fragment %s: %w", file, err)
        }

        // Validate against schema
        if fl.schema != nil {
            if err := fl.schema.Validate(fragment.Raw); err != nil {
                // Quarantine the fragment
                fl.quarantined = append(fl.quarantined, &QuarantinedFragment{
                    SourceFile:      file,
                    Raw:             fragment.Raw,
                    QuarantineReasons: []string{err.(*jsonschema.ValidationError).String()},
                })
                continue
            }
        }

        // Validate Go-side rules (cross-path, manifest, etc.)
        if errs := validateGoRules(fragment); len(errs) > 0 {
            fl.quarantined = append(fl.quarantined, &QuarantinedFragment{
                SourceFile:      file,
                Raw:             fragment.Raw,
                QuarantineReasons: errs,
            })
            continue
        }

        fl.fragments = append(fl.fragments, fragment)
    }

    return nil
}
```

### 4.3 Validating Cross-Field Rules (Go Side)

```go
package spec

import (
    "fmt"
    "strings"
)

// validateGoRules enforces §4 cross-field, cross-path, and manifest rules
func validateGoRules(fragment *Fragment) []string {
    var errs []string

    // 4.1: x-instance-param must be in every path
    if fragment.InstanceParam != "" {
        for path := range fragment.Paths {
            if !strings.Contains(path, "{"+fragment.InstanceParam+"}") {
                errs = append(errs, fmt.Sprintf(
                    "x-instance-param '%s' not found in path '%s'",
                    fragment.InstanceParam, path,
                ))
            }
        }
    }

    // 4.1: x-upstream-strip-prefix must be a prefix of every path
    if fragment.UpstreamStripPrefix != "" {
        for path := range fragment.Paths {
            if !strings.HasPrefix(path, fragment.UpstreamStripPrefix) {
                errs = append(errs, fmt.Sprintf(
                    "x-upstream-strip-prefix '%s' is not a prefix of path '%s'",
                    fragment.UpstreamStripPrefix, path,
                ))
            }
        }
    }

    // 4.1: Reserved control-plane namespace
    reservedPrefixes := []string{"/docs/", "/health/", "/config/", "/approvals/", "/_seam/"}
    for path := range fragment.Paths {
        for _, prefix := range reservedPrefixes {
            if strings.HasPrefix(path, prefix) {
                errs = append(errs, fmt.Sprintf(
                    "path '%s' uses reserved prefix '%s'",
                    path, prefix,
                ))
            }
        }
    }

    // 4.4: x-seam-owner must match mounted directory (checked at load time)
    // This is enforced by the loader, not the fragment validator

    return errs
}
```

### 4.4 Merge-Time Collision Detection

```go
package spec

import (
    "fmt"
)

// CollisionKey is the tuple (path, method, x-api-version)
type CollisionKey struct {
    Path      string
    Method    string
    APIVersion string
}

// MergeFragments merges all valid fragments and detects collisions
func (fl *FragmentLoader) MergeFragments() (map[string]interface{}, error) {
    merged := make(map[string]interface{})
    collisionKeys := make(map[CollisionKey]string) // key -> fragment source

    for _, fragment := range fl.fragments {
        apiVersion := fragment.APIVersion
        if apiVersion == "" {
            apiVersion = "_unversioned"
        }

        for path, pathItem := range fragment.Paths {
            for method, operation := range pathItem.Operations() {
                key := CollisionKey{
                    Path:      path,
                    Method:    method,
                    APIVersion: apiVersion,
                }

                // Check for collision
                if existing, exists := collisionKeys[key]; exists {
                    // Collision! The later fragment loses.
                    // Log and continue (deterministic by filename order)
                    fmt.Printf("⚠️  Collision detected: %s %s (version %s)\n", 
                        method, path, apiVersion)
                    fmt.Printf("   Incumbent: %s\n", existing)
                    fmt.Printf("   Challenger: %s (rejected)\n", fragment.Source)
                    continue
                }

                collisionKeys[key] = fragment.Source
            }
        }

        // Merge fragment into document
        mergeFragment(merged, fragment)
    }

    return merged, nil
}
```

---

## Part 5: Testing and Conformance

### 5.1 Schema Validation Corpus

The schema is verified by a fixture suite (see `route-fragment-schema.md` §7). The corpus lives in `~/scratch/seam-schema-verify/` until Phase 9a provides a checked-in home.

**Run the conformance tests:**

```bash
cd ~/scratch/seam-schema-verify
node harness.js
```

**Expected output:**

```
✅ Schema meta-validates against 2020-12 meta-schema
✅ Schema compiles with no dangling $ref
✅ Accepts valid single-instance fragment
✅ Accepts valid multi-instance map
✅ Accepts valid adapter fragment
✅ Rejects unpaired vault/inject
✅ Rejects map without instance-param
✅ Rejects upstream + map together
✅ Rejects adapter with upstream fields
✅ Rejects http:// without plaintext acknowledgment
✅ Rejects quota without cost-per-call
...

31/31 checks green
```

### 5.2 Go Validator Tests

Tests in `internal/spec/fragment_test.go` verify that the Go validator enforces §4 rules:

```go
package spec

import (
    "testing"
)

func TestValidateGoRules_InstanceParamInAllPaths(t *testing.T) {
    fragment := &Fragment{
        InstanceParam: "cluster",
        Paths: map[string]*PathItem{
            "/k8s/{cluster}/pods":    {},
            "/k8s/{cluster}/nodes":   {},
            "/k8s/{namespace}/pods":  {}, // Missing {cluster}
        },
    }

    errs := validateGoRules(fragment)
    if len(errs) != 1 {
        t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
    }

    expected := "x-instance-param 'cluster' not found in path '/k8s/{namespace}/pods'"
    if errs[0] != expected {
        t.Errorf("expected error %q, got %q", expected, errs[0])
    }
}

func TestValidateGoRules_ReservedNamespace(t *testing.T) {
    fragment := &Fragment{
        Paths: map[string]*PathItem{
            "/docs/argo": {}, // Reserved prefix
            "/api/v1":     {},
        },
    }

    errs := validateGoRules(fragment)
    if len(errs) != 1 {
        t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
    }

    expected := "path '/docs/argo' uses reserved prefix '/docs/'"
    if errs[0] != expected {
        t.Errorf("expected error %q, got %q", expected, errs[0])
    }
}
```

---

## Part 6: Migration Checklist

If migrating from JSON Schema → Go structs:

- [ ] Evaluate: Is struct generation necessary? (Current answer: No)
- [ ] If yes, choose generator: `go-jsonschema` or `jsonschema-gen`
- [ ] Generate structs in `internal/spec/fragment_gen.go`
- [ ] Add `// Code generated by ... DO NOT EDIT.` header
- [ ] Implement `ValidateFragment()` using generated structs
- [ ] Port §4 Go-side rules to struct methods
- [ ] Add tests for struct-based validation
- [ ] Update integration guide to reflect struct usage
- [ ] Keep JSON Schema as source of truth (regenerate on schema change)

---

## Appendix: Quick Reference

### A. Schema Location

```
docs/notes/route-fragment-schema.json
```

### B. Validator Package

```
internal/spec/
├── loader.go          # Fragment loading and merging
├── fragment.go        # Fragment validation (planned)
└── fragment_test.go   # Validator tests
```

### C. Key Files

| File | Purpose |
|------|---------|
| `route-fragment-schema.json` | JSON Schema 2020-12 contract |
| `route-fragment-schema.md` | Human authority (placement, cross-field rules) |
| `loader.go` | Fragment loading and runtime validation |
| `validation.go` | Request validation middleware |
| `language-runtime-choice.md` | ADR-001 research |

### D. Phase Mapping

| Phase | Schema Use | Implementation Status |
|-------|-----------|----------------------|
| Phase 1b (runtime quarantine) | Admission control at load time | ✅ Partial (loader.go) |
| Phase 9a (seam lint) | CI validation before commit | ⏳ Planned (cmd/seam lint) |

### E. Error Categories

| Category | Caught By | Example |
|----------|-----------|---------|
| Shape violation | JSON Schema | `x-quota` without `x-cost-per-call` |
| Intra-object violation | JSON Schema | `x-adapter` with `x-upstream` |
| Cross-path violation | Go validator | Instance param not in all paths |
| Manifest violation | Go validator | Upstream not in allowlist |
| Collision | Merge time | Duplicate `(path, method, version)` |

---

**Document version:** 1.0  
**Last updated:** 2026-08-07  
**Next review:** Phase 9a implementation start
