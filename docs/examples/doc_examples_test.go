// Package docexamples_test gates the checked-in documentation example trees
// on the same validation the gateway applies to production fragments. The
// examples under docs/examples and examples teach the fragment grammar by
// hand, and until this gate existed nothing re-checked them: the grammar
// moved to unit-bearing x-cost-per-call objects, amount/unit/window x-quota,
// and maxRepeats/window x-loop-guard while the docs still taught bare
// numbers, limit/window_seconds, and max_iterations/backoff_ms. This test is
// the CI hook that fails on the next drift instead of on the next reader.
package docexamples

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	seamspec "github.com/ardenone/seam/internal/spec"
)

// ownerDirectoryPlacement is the one lint code exempted here, and only for
// documentation copies.
const ownerDirectoryPlacement = "owner.directory-mismatch"

// TestCheckedInDocumentationExamplesStayLintClean parses every YAML/JSON file
// in each documentation example tree, validates it against the repo's
// route-fragment schema, and runs the full seam lint rule set over it — the
// same LintDirectory entry point the seam-lint CI lane runs against
// fragments/argocd-ro. A stale extension shape, a broken envelope, or an
// x-vault-path that stops nesting its x-seam-owner fails the build here.
func TestCheckedInDocumentationExamplesStayLintClean(t *testing.T) {
	for _, tree := range documentationTrees() {
		t.Run(tree.name, func(t *testing.T) {
			checkExampleTree(t, tree.name, tree.dir)
		})
	}
}

// documentationTrees returns the checked-in documentation estates this gate
// guards. Both gates below — lint cleanliness and credential-reference
// hygiene — must walk exactly these trees, so the list lives in one place.
func documentationTrees() []struct {
	name string
	dir  string
} {
	return []struct {
		name string
		dir  string
	}{
		{name: "docs/examples", dir: "."},
		{name: "examples", dir: filepath.Join("..", "..", "examples")},
	}
}

func checkExampleTree(t *testing.T, name, dir string) {
	t.Helper()

	want := fragmentFileCount(t, dir)
	if want == 0 {
		t.Fatalf("no .json/.yaml/.yml files under %s - the documentation tree is missing", name)
	}

	report, err := seamspec.LintDirectory(seamspec.LintOptions{
		FragmentsDir: dir,
		SchemaPath:   filepath.Join("..", "..", "spec", "route-fragment-schema.json"),
	})
	if err != nil {
		t.Fatalf("LintDirectory(%s): %v", name, err)
	}
	if report.Files != want {
		t.Fatalf("linted %d files but found %d under %s - the lint walk and the tree disagree", report.Files, want, name)
	}

	for _, finding := range report.Errors {
		if finding.Code == ownerDirectoryPlacement {
			// The placement rule encodes the production fragments-tree
			// convention: a fragment lives at fragments/<owner>/<file>,
			// so its parent directory must equal x-seam-owner.
			// Documentation copies live under a documentation topic
			// instead, so that half of the owner chain cannot apply.
			// The staleness half still does — owner.vault-path-mismatch
			// fires when an example's x-vault-path stops nesting its
			// x-seam-owner, and that code is not exempted.
			continue
		}
		t.Errorf("%s: [%s] %s", finding.File, finding.Code, finding.Message)
	}
	for _, finding := range report.Warnings {
		t.Logf("warning %s: [%s] %s", finding.File, finding.Code, finding.Message)
	}
}

// fragmentFileCount independently enumerates the tree so a silent walk change
// (a renamed directory, a new extension) cannot turn this gate into a no-op
// that linted zero files and reported success.
func fragmentFileCount(t *testing.T, dir string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json", ".yaml", ".yml":
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return count
}

// credentialFieldKeys are the fragment-root and upstream-map-entry fields
// that name a credential. docs/notes/credential-reference-syntax.md defines
// the fragment surface's one legal shape: a scheme-less bare path.
var credentialFieldKeys = map[string]bool{
	"x-vault-path": true, // fragment root
	"vaultPath":    true, // x-upstream-map entry
}

// injectFieldKeys are the fields that carry injection metadata. The schema
// closes them to kind/name, so anything else — above all a value — is a
// literal credential smuggled past the pair.
var injectFieldKeys = map[string]bool{
	"x-inject-as": true, // fragment root
	"injectAs":    true, // x-upstream-map entry
}

// literalCredentialPatterns detect a credential *value* rather than a
// reference: provider-token prefixes with provider-specific payload shapes,
// a JWT's three base64url segments, and a raw alphanumeric run long enough
// that no path segment or prose word collides with it. All three are
// verified absent from the current trees; a new example that pastes a real
// token fails here instead of shipping in documentation.
var literalCredentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{16,}`),                                           // OpenAI-style
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),                                    // GitHub
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),                                  // GitHub fine-grained
	regexp.MustCompile(`\bxox[abpsr]-[A-Za-z0-9-]{10,}`),                                  // Slack
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}`),                                      // GitLab
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                            // AWS access key
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),                                       // Google API key
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), // JWT
	regexp.MustCompile(`\b[A-Za-z0-9]{32,}\b`),                                            // raw high-entropy run
}

// TestDocumentationCredentialFieldsStayReferences pins the
// credential-reference boundary (docs/notes/credential-reference-syntax.md)
// on the documentation estate. The lint gate above validates the fragment
// grammar; this walk validates what no schema reaches: that every credential
// field across both trees still resolves to a scheme-less path reference,
// that injection metadata never grows a literal value field, and that no
// prose, description, or example payload string anywhere carries a
// literal-shaped credential. Docs teach by copy-paste; a token pasted into
// an example is a leaked credential, and a path rewritten into the corpus's
// vault:-schemed form is a shape the runtime refuses at load.
func TestDocumentationCredentialFieldsStayReferences(t *testing.T) {
	for _, tree := range documentationTrees() {
		t.Run(tree.name, func(t *testing.T) {
			checkCredentialReferences(t, tree.name, tree.dir)
		})
	}
}

func checkCredentialReferences(t *testing.T, name, dir string) {
	t.Helper()

	files := 0
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json", ".yaml", ".yml":
		default:
			return nil
		}
		files++

		contents, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: read: %v", path, err)
			return nil
		}
		var decoded any
		if err := yaml.Unmarshal(contents, &decoded); err != nil {
			t.Errorf("%s: parse: %v", path, err)
			return nil
		}
		normalized, err := normalizeDocValue(decoded)
		if err != nil {
			t.Errorf("%s: normalize: %v", path, err)
			return nil
		}
		scanCredentialStrings(t, path, "", "", normalized)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", name, err)
	}
	if files == 0 {
		t.Fatalf("no .json/.yaml/.yml files under %s - the documentation tree is missing", name)
	}
}

// scanCredentialStrings walks one parsed document, carrying the enclosing
// key so credential-naming fields can be held to the reference shape while
// every other string is swept for literal credential shapes.
func scanCredentialStrings(t *testing.T, file, path, key string, value any) {
	t.Helper()

	switch {
	case credentialFieldKeys[key]:
		text, ok := value.(string)
		if !ok {
			t.Errorf("%s: %s: credential field must be a path string, got %T", file, fieldPath(path, key), value)
			return
		}
		if strings.HasPrefix(text, "vault:") {
			t.Errorf("%s: %s: carries the vault: scheme - that form belongs to the differential corpus's secret refs, the fragment surface is the bare path (docs/notes/credential-reference-syntax.md): %q", file, fieldPath(path, key), text)
		}
		segments := strings.Split(text, "/")
		if len(segments) < 3 || containsEmpty(segments) {
			t.Errorf("%s: %s: is not a <base>/<owner>/<name> path reference - a pasted literal cannot name a vault KV path: %q", file, fieldPath(path, key), text)
		}
		return

	case injectFieldKeys[key]:
		fields, ok := value.(map[string]any)
		if !ok {
			t.Errorf("%s: %s: injection metadata must be an object, got %T", file, fieldPath(path, key), value)
			return
		}
		for field := range fields {
			switch field {
			case "kind", "name":
				// The only members the schema allows: where and how to
				// inject, never what.
			default:
				t.Errorf("%s: %s: injection metadata carries %q - a literal credential value must never ride beside kind/name", file, fieldPath(path, key), field)
			}
		}
		return
	}

	switch value := value.(type) {
	case map[string]any:
		for childKey, child := range value {
			scanCredentialStrings(t, file, fieldPath(path, key), childKey, child)
		}
	case []any:
		for _, child := range value {
			scanCredentialStrings(t, file, path, key, child)
		}
	case string:
		if key == "" {
			return
		}
		for _, pattern := range literalCredentialPatterns {
			if match := pattern.FindString(value); match != "" {
				t.Errorf("%s: %s: string for %q contains a literal-shaped credential %q - replace it with a reference", file, fieldPath(path, key), key, match)
				return
			}
		}
	}
}

// fieldPath renders a JSON-pointer-style location for error messages,
// skipping the empty document root.
func fieldPath(path, key string) string {
	if path == "" {
		return "/" + key
	}
	if key == "" {
		return path
	}
	return path + "/" + key
}

func containsEmpty(segments []string) bool {
	for _, segment := range segments {
		if segment == "" {
			return true
		}
	}
	return false
}

// driftNoScrubPattern matches the blanket no-scrubbing claim the
// complex-route example used to teach ("Responses are not scrubbed - may
// contain echoed secrets"). The landed x-unscrubbable contract
// (docs/notes/x-unscrubbable-contract.md; pinned by seam-1e05bb3e)
// design-contradicts that premise: the acknowledgement changes exactly one
// runtime behavior — an *unscannable* response is passed through whole
// instead of refused — and every response the proxy can scan is scrubbed on
// an acknowledged route exactly as on an ordinary one. The contract's own
// vocabulary for the pass-through is "unsanitized", so "not scrubbed" /
// "never scrubbed" has no true use anywhere in the documentation estate.
var driftNoScrubPattern = regexp.MustCompile(`(?i)\b(?:not|never)\s+scrubbed\b`)

// driftDeprecationHeaderPattern matches the X- prefixed deprecation-family
// response-header names. The gateway emits the unprefixed RFC 9745
// Deprecation and RFC 8594 Sunset fields plus Link rel=deprecation links
// (internal/server/deprecation_middleware.go), and
// docs/notes/brownout-runtime-semantics.md ("Header names: no X- prefix")
// records that callers looking for X-Deprecation/X-Sunset will not find
// them — RFC 6648 deprecates the prefix for new fields.
var driftDeprecationHeaderPattern = regexp.MustCompile(`(?i)^x-(?:deprecation|sunset)$`)

// httpMethodKeys are the OpenAPI path-item operation keys.
var httpMethodKeys = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// TestDocumentationExamplesTeachLandedExtensionSemantics pins the two
// extension-semantics contracts that no schema rule reaches: prose and
// response-header declarations are grammar-valid OpenAPI, so the lint gate
// above passes straight past them while still teaching callers something the
// gateway does not do. It walks every .json/.yaml/.yml file under
// docs/examples (including route-fragments/) and fails on:
//
//   - a response-header declaration named X-Deprecation/X-Sunset: the
//     gateway never emits an X- prefixed deprecation header, so an example
//     advertising one on a documented response teaches a header that does
//     not exist;
//   - any string asserting responses are "not scrubbed"/"never scrubbed":
//     the seam-1e05bb3e-contradicted premise. Acknowledgement is not a
//     scrubbing opt-out;
//   - an x-unscrubbable: "acknowledged" site (fragment root or operation,
//     the two placements the runtime accepts) whose sibling description
//     never names the unscannable scope: an example that acknowledges
//     without stating what the acknowledgement actually does teaches the
//     same omission by silence.
//
// Only docs/examples is walked. The examples/ tree also declares
// X-Deprecation/X-Sunset response headers, but brownout-runtime-semantics.md
// already frames those as what the example API advertises in its own
// response declarations rather than a gateway-emission claim — a documented
// divergence, and a different estate from the one this gate owns.
func TestDocumentationExamplesTeachLandedExtensionSemantics(t *testing.T) {
	checkExtensionSemantics(t, "docs/examples", ".")
}

func checkExtensionSemantics(t *testing.T, name, dir string) {
	t.Helper()

	files := 0
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json", ".yaml", ".yml":
		default:
			return nil
		}
		files++

		contents, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: read: %v", path, err)
			return nil
		}
		var decoded any
		if err := yaml.Unmarshal(contents, &decoded); err != nil {
			t.Errorf("%s: parse: %v", path, err)
			return nil
		}
		normalized, err := normalizeDocValue(decoded)
		if err != nil {
			t.Errorf("%s: normalize: %v", path, err)
			return nil
		}
		doc, ok := normalized.(map[string]any)
		if !ok {
			t.Errorf("%s: top-level document is %T, want an object", path, normalized)
			return nil
		}
		scrubDescriptionsForDrift(t, path, "", doc)
		checkDeprecationHeaderNames(t, path, doc)
		checkAcknowledgementDescriptions(t, path, doc)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", name, err)
	}
	if files == 0 {
		t.Fatalf("no .json/.yaml/.yml files under %s - the documentation tree is missing", name)
	}
}

// scrubDescriptionsForDrift sweeps every string in the document for the
// blanket no-scrubbing claim, wherever it hides: operation descriptions,
// schema property descriptions, summaries, examples.
func scrubDescriptionsForDrift(t *testing.T, file, path string, value any) {
	t.Helper()

	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			scrubDescriptionsForDrift(t, file, fieldPath(path, key), child)
		}
	case []any:
		for i, child := range value {
			scrubDescriptionsForDrift(t, file, fieldPath(path, fmt.Sprint(i)), child)
		}
	case string:
		if match := driftNoScrubPattern.FindString(value); match != "" {
			t.Errorf("%s: %s: claims %q - the x-unscrubbable contract (docs/notes/x-unscrubbable-contract.md) is not a scrubbing opt-out: an acknowledgement only lets an unscannable response pass through instead of being refused, and every scannable response is still scrubbed", file, path, match)
		}
	}
}

// checkDeprecationHeaderNames rejects X- prefixed deprecation-family
// response-header declarations anywhere in the paths object.
func checkDeprecationHeaderNames(t *testing.T, file string, doc map[string]any) {
	t.Helper()

	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		return
	}
	for path, item := range paths {
		pathItem, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for method, op := range pathItem {
			if !httpMethodKeys[strings.ToLower(method)] {
				continue
			}
			operation, ok := op.(map[string]any)
			if !ok {
				continue
			}
			responses, ok := operation["responses"].(map[string]any)
			if !ok {
				continue
			}
			for status, resp := range responses {
				response, ok := resp.(map[string]any)
				if !ok {
					continue
				}
				headers, ok := response["headers"].(map[string]any)
				if !ok {
					continue
				}
				for header := range headers {
					if driftDeprecationHeaderPattern.MatchString(header) {
						t.Errorf("%s: /paths/%s/%s/responses/%s/headers/%s: declares an X- prefixed deprecation header - the gateway emits the unprefixed RFC 9745 Deprecation and RFC 8594 Sunset fields and never an X- prefixed form (docs/notes/brownout-runtime-semantics.md, \"Header names: no X- prefix\")", file, path, method, status, header)
					}
				}
			}
		}
	}
}

// checkAcknowledgementDescriptions requires every x-unscrubbable:
// "acknowledged" site — fragment root or operation, the two placements
// extractAcknowledgedExtension reads — to state the acknowledgement's actual
// scope in its sibling description. "unscannable" is the contract's own term
// of art for what the acknowledgement covers, so its presence is the
// cheapest structural proof the prose teaches the landed behavior rather
// than the refuted one.
func checkAcknowledgementDescriptions(t *testing.T, file string, doc map[string]any) {
	t.Helper()

	if doc["x-unscrubbable"] == "acknowledged" {
		description := ""
		if info, ok := doc["info"].(map[string]any); ok {
			description, _ = info["description"].(string)
		}
		requireUnscannableScope(t, file, "/x-unscrubbable", "/info/description", description)
	}

	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		return
	}
	for path, item := range paths {
		pathItem, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for method, op := range pathItem {
			if !httpMethodKeys[strings.ToLower(method)] {
				continue
			}
			operation, ok := op.(map[string]any)
			if !ok {
				continue
			}
			if operation["x-unscrubbable"] != "acknowledged" {
				continue
			}
			description, _ := operation["description"].(string)
			requireUnscannableScope(t, file, "/paths/"+path+"/"+method+"/x-unscrubbable", "/description", description)
		}
	}
}

func requireUnscannableScope(t *testing.T, file, site, descriptionPath, description string) {
	t.Helper()

	if !strings.Contains(strings.ToLower(description), "unscannable") {
		t.Errorf("%s: %s: acknowledges x-unscrubbable without stating its scope - the acknowledgement covers unscannable responses only (opaque media types, unsupported Content-Encoding, protocol upgrades are passed through instead of refused); scannable responses are still scrubbed. Say so in %s.", file, site, descriptionPath)
	}
}

// exampleHistoricalMarker marks a documentation fragment as a historical
// teaching artifact: its x-seam-deprecated dates depict a completed
// deprecation lifecycle and are exempt from the live-sunset requirement of
// TestDocumentationDeprecationExamplesStayLive. The marker is an inert
// OpenAPI root extension — nothing in the runtime or in seam lint reads it;
// this gate is its only reader, which is also what makes it a promise the
// gate can hold the fragment to.
const exampleHistoricalMarker = "x-seam-example-historical"

// sunsetDayEnd returns the instant the sunset date's calendar day ends in
// UTC — the same boundary checkDeprecation's range check applies when it
// judges a brownout instant to lie inside [since, sunset] — so this gate and
// the linter agree on when a sunset has passed. ok is false for a string
// that is not an ISO date; the format rules belong to the lint gate
// (deprecation.sunset-invalid), not here.
func sunsetDayEnd(sunset string) (time.Time, bool) {
	day, err := time.Parse("2006-01-02", sunset)
	if err != nil {
		return time.Time{}, false
	}
	return day.AddDate(0, 0, 1), true
}

// TestDocumentationDeprecationExamplesStayLive keeps the documentation
// estates' deprecation examples from silently expiring. Both trees teach by
// copy-paste: a reader who lifts a "live" example whose sunset has already
// passed ships a route that is born fully deprecated, and before this gate
// nothing failed when wall-clock time walked past a checked-in date — the
// complex-route example sat with a July 2026 sunset and June 2026 brownout
// windows until a strand noticed in September 2026, and the multi-instance
// example in examples/fragments had aged the same way. For every
// x-seam-deprecated block at the two placements the runtime reads (fragment
// root and path item — the same walk checkDeprecation uses), a parseable
// sunset must agree with the frame the fragment declares:
//
//   - a live fragment (no marker) whose sunset has passed has expired:
//     refresh the dates, or — if it now teaches a completed lifecycle —
//     mark the fragment root x-seam-example-historical: true;
//   - a marked-historical fragment whose sunset is still in the future
//     contradicts its own marker: a historical artifact's lifecycle is
//     over, so either drop the marker or move the sunset into the past.
//
// since is deliberately unchecked: even a live example normally carries a
// past since, because a deprecation begins before it sunsets. The failure
// is deliberately time-dependent — it fires the day the sunset passes.
// That is the tripwire, not a flake: a documentation example is the one
// fixture in the repo whose correctness is judged against the calendar, and
// the gate turns that from silent rot into a maintenance signal
// (docs/notes/brownout-runtime-semantics.md, "Documentation examples stay
// live").
func TestDocumentationDeprecationExamplesStayLive(t *testing.T) {
	for _, tree := range documentationTrees() {
		t.Run(tree.name, func(t *testing.T) {
			checkDeprecationFreshness(t, tree.name, tree.dir)
		})
	}
}

func checkDeprecationFreshness(t *testing.T, name, dir string) {
	t.Helper()

	files := 0
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json", ".yaml", ".yml":
		default:
			return nil
		}
		files++

		contents, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: read: %v", path, err)
			return nil
		}
		var decoded any
		if err := yaml.Unmarshal(contents, &decoded); err != nil {
			t.Errorf("%s: parse: %v", path, err)
			return nil
		}
		normalized, err := normalizeDocValue(decoded)
		if err != nil {
			t.Errorf("%s: normalize: %v", path, err)
			return nil
		}
		doc, ok := normalized.(map[string]any)
		if !ok {
			t.Errorf("%s: top-level document is %T, want an object", path, normalized)
			return nil
		}
		historical := doc[exampleHistoricalMarker] == true
		checkSunsetFreshness(t, path, "/x-seam-deprecated", historical, doc["x-seam-deprecated"])
		paths, ok := doc["paths"].(map[string]any)
		if !ok {
			return nil
		}
		for pathKey, item := range paths {
			pathItem, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if block, present := pathItem["x-seam-deprecated"]; present {
				checkSunsetFreshness(t, path, "/paths/"+pathKey+"/x-seam-deprecated", historical, block)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", name, err)
	}
	if files == 0 {
		t.Fatalf("no .json/.yaml/.yml files under %s - the documentation tree is missing", name)
	}
}

// checkSunsetFreshness applies the live/historical rule to one
// x-seam-deprecated block. Blocks that are missing, carry no sunset, or
// carry an unparseable one are skipped: without a sunset nothing on the
// block can expire, and the shape rules are the lint gate's job.
func checkSunsetFreshness(t *testing.T, file, site string, historical bool, block any) {
	t.Helper()

	blockMap, ok := block.(map[string]any)
	if !ok {
		return
	}
	sunset, ok := blockMap["sunset"].(string)
	if !ok || sunset == "" {
		return
	}
	dayEnd, ok := sunsetDayEnd(sunset)
	if !ok {
		return
	}
	expired := !time.Now().UTC().Before(dayEnd)
	switch {
	case expired && !historical:
		t.Errorf("%s: %s: sunset %s has passed - this example presents itself as live, and a reader who copies it ships a route that is born fully deprecated. Refresh the dates (brownout windows ordered and inside the new [since, sunset]) or, if it now teaches a completed lifecycle, mark the fragment root %s: true", file, site, sunset, exampleHistoricalMarker)
	case !expired && historical:
		t.Errorf("%s: %s: sunset %s is still in the future but the fragment is marked %s: true - the marker and the dates contradict each other. A historical artifact's lifecycle is over, so either drop the marker or move the sunset into the past", file, site, sunset, exampleHistoricalMarker)
	}
}

// normalizeDocValue makes yaml.v3's map[any]any representation safe for the
// string walk, mirroring internal/spec's lint normalizer.
func normalizeDocValue(value any) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			normalized, err := normalizeDocValue(child)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case map[any]any:
		result := make(map[string]any, len(value))
		for rawKey, child := range value {
			key, ok := rawKey.(string)
			if !ok {
				return nil, fs.ErrInvalid
			}
			normalized, err := normalizeDocValue(child)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(value))
		for i, child := range value {
			normalized, err := normalizeDocValue(child)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}
