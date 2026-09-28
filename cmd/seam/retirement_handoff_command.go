package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ardenone/seam/internal/spec"
	"go.yaml.in/yaml/v4"
)

const retirementFindingMessage = "Deprecation candidate detected"

// retirementFinding is the safe, actionable subset of an evaluator log
// record. fragment_path remains a locator; --target names the exact local
// fragment or ConfigMap manifest the operator has reviewed.
type retirementFinding struct {
	Route          string
	APIVersion     string
	SpecVersion    string
	FragmentPath   string
	DeprecatedYAML string
}

type retirementTarget struct {
	path      string
	dataKey   string
	configMap bool
	document  *yaml.Node
	fragment  []byte
	rendered  []byte
	owner     string
}

type hotReloadStatus struct {
	Enabled      bool   `json:"enabled"`
	ReloadCount  uint64 `json:"reload_count"`
	FailureCount uint64 `json:"failure_count"`
	LastReload   string `json:"last_reload_time"`
}

type retirementHealthResponse struct {
	HotReload  *hotReloadStatus `json:"hot_reload"`
	RouteTable struct {
		HotReload *hotReloadStatus `json:"hot_reload"`
	} `json:"route_table"`
}

// retirementHandoffCommand is an operator command, not part of the
// evaluator. It makes the human handoff repeatable while keeping the
// evaluator's proposal-only contract intact.
func retirementHandoffCommand(args []string) {
	os.Exit(runRetirementHandoffCommand(args, os.Stdin, os.Stdout, os.Stderr, http.DefaultClient))
}

func runRetirementHandoffCommand(args []string, stdin io.Reader, stdout, stderr io.Writer, client *http.Client) int {
	fs := flag.NewFlagSet("retirement-handoff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var findingPath, targetPath, dataKey, schemaPath, allowlistPath, observeURL string
	var apply, observeOnly bool
	var observeTimeout, observeInterval time.Duration
	fs.StringVar(&findingPath, "finding", "", "Evaluator JSON record or JSON-lines log file; - reads stdin")
	fs.StringVar(&targetPath, "target", "", "Exact fragment file or ConfigMap manifest to prepare")
	fs.StringVar(&dataKey, "data-key", "", "ConfigMap data key containing the fragment")
	fs.StringVar(&schemaPath, "schema", "spec/route-fragment-schema.json", "Route fragment schema used by the pre-land lint gate")
	fs.StringVar(&allowlistPath, "upstream-allowlist", "", "Optional operator-owned upstream allowlist for lint")
	fs.BoolVar(&apply, "apply", false, "Write the linted proposal to --target; default is a no-write plan")
	fs.StringVar(&observeURL, "observe-url", "", "Optional full URL of /health/upstreams or /config/status to observe hot reload")
	fs.BoolVar(&observeOnly, "observe-only", false, "Wait for the next successful hot reload; use after the manifest is pushed")
	fs.DurationVar(&observeTimeout, "observe-timeout", 30*time.Second, "Maximum time to wait for a successful hot reload")
	fs.DurationVar(&observeInterval, "observe-interval", time.Second, "Delay between hot-reload observations")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if observeTimeout <= 0 || observeInterval <= 0 {
		_, _ = fmt.Fprintln(stderr, "retirement-handoff: observe durations must be positive")
		return 2
	}
	if observeOnly {
		if observeURL == "" {
			_, _ = fmt.Fprintln(stderr, "retirement-handoff: --observe-only requires --observe-url")
			return 2
		}
		before, err := readHotReloadStatus(clientOrDefault(client), observeURL)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "retirement-handoff: hot-reload baseline: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "waiting for hot reload after reload_count=%d\n", before.ReloadCount)
		after, err := waitForRetirementHotReload(clientOrDefault(client), observeURL, before, observeTimeout, observeInterval)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "retirement-handoff: observe hot reload: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "hot reload observed: reload_count=%d failure_count=%d last_reload_time=%s\n", after.ReloadCount, after.FailureCount, after.LastReload)
		return 0
	}
	if findingPath == "" || targetPath == "" {
		_, _ = fmt.Fprintln(stderr, "retirement-handoff: --finding and --target are required")
		fs.PrintDefaults()
		return 2
	}
	if observeURL != "" && !apply {
		_, _ = fmt.Fprintln(stderr, "retirement-handoff: --observe-url requires --apply")
		return 2
	}
	client = clientOrDefault(client)

	finding, err := readRetirementFinding(findingPath, stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "retirement-handoff: read finding: %v\n", err)
		return 1
	}
	target, err := prepareRetirementTarget(targetPath, dataKey, finding.DeprecatedYAML)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "retirement-handoff: prepare target: %v\n", err)
		return 1
	}
	report, err := lintRetirementFragment(target.fragment, target.owner, schemaPath, allowlistPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "retirement-handoff: pre-land lint: %v\n", err)
		return 1
	}
	if report.HasErrors() {
		_, _ = fmt.Fprintln(stderr, "pre-land lint failed; target was not written")
		writeLintText(stderr, report)
		return 1
	}

	var before hotReloadStatus
	if observeURL != "" {
		before, err = readHotReloadStatus(client, observeURL)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "retirement-handoff: hot-reload baseline: %v\n", err)
			return 1
		}
	}

	if apply {
		if err := writeRetirementTarget(target); err != nil {
			_, _ = fmt.Fprintf(stderr, "retirement-handoff: apply: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "applied linted retirement proposal for %s to %s\n", finding.Route, target.path)
		_, _ = fmt.Fprintln(stdout, "revert: after landing this file change, use `git revert <landing-commit>` in declarative-config; do not mutate the live object")
	} else {
		_, _ = fmt.Fprintf(stdout, "plan: linted retirement proposal for %s (spec %s); target %s was not written\n", finding.Route, finding.SpecVersion, target.path)
		_, _ = fmt.Fprintln(stdout, "next: rerun with --apply after reviewing the diff, then commit and push the declarative-config change")
	}
	_, _ = fmt.Fprintf(stdout, "lint: passed (%d file(s), %d warning(s))\n", report.Files, len(report.Warnings))

	if observeURL != "" {
		after, err := waitForRetirementHotReload(client, observeURL, before, observeTimeout, observeInterval)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "retirement-handoff: observe hot reload: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "hot reload observed: reload_count=%d failure_count=%d last_reload_time=%s\n", after.ReloadCount, after.FailureCount, after.LastReload)
	}
	return 0
}

func clientOrDefault(client *http.Client) *http.Client {
	if client == nil {
		return http.DefaultClient
	}
	return client
}

func readRetirementFinding(path string, stdin io.Reader) (retirementFinding, error) {
	var contents []byte
	var err error
	if path == "-" {
		contents, err = io.ReadAll(stdin)
	} else {
		contents, err = os.ReadFile(path)
	}
	if err != nil {
		return retirementFinding{}, err
	}

	lines := []string{string(contents)}
	if bytes.Contains(contents, []byte("\n")) {
		lines = strings.Split(string(contents), "\n")
	}
	var lastErr error
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			lastErr = err
			continue
		}
		var message string
		if err := json.Unmarshal(raw["msg"], &message); err != nil || message != retirementFindingMessage {
			continue
		}
		var finding retirementFinding
		for key, destination := range map[string]*string{
			"route":                   &finding.Route,
			"api_version":             &finding.APIVersion,
			"spec_version":            &finding.SpecVersion,
			"fragment_path":           &finding.FragmentPath,
			"x_seam_deprecated_block": &finding.DeprecatedYAML,
		} {
			if err := json.Unmarshal(raw[key], destination); err != nil || strings.TrimSpace(*destination) == "" {
				return retirementFinding{}, fmt.Errorf("finding field %q is missing or not a non-empty string", key)
			}
		}
		if err := validateRetirementBlock(finding.DeprecatedYAML); err != nil {
			return retirementFinding{}, err
		}
		return finding, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no deprecation candidate record found")
	}
	return retirementFinding{}, lastErr
}

func validateRetirementBlock(raw string) error {
	document, err := parseYAMLDocument([]byte(raw))
	if err != nil {
		return fmt.Errorf("x_seam_deprecated_block is not valid YAML: %w", err)
	}
	root := yamlDocumentRoot(document)
	if root == nil || root.Kind != yaml.MappingNode {
		return errors.New("x_seam_deprecated_block must have a mapping root")
	}
	if _, ok := mappingValue(root, "x-seam-deprecated"); !ok {
		return errors.New("x_seam_deprecated_block must contain an x-seam-deprecated root key")
	}
	return nil
}

func prepareRetirementTarget(path, dataKey, block string) (retirementTarget, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return retirementTarget{}, err
	}
	document, err := parseYAMLDocument(contents)
	if err != nil {
		return retirementTarget{}, fmt.Errorf("parse %q: %w", path, err)
	}
	root := yamlDocumentRoot(document)
	if root == nil || root.Kind != yaml.MappingNode {
		return retirementTarget{}, fmt.Errorf("%q does not contain a YAML mapping", path)
	}

	target := retirementTarget{path: path, dataKey: dataKey, document: document}
	fragmentRoot := root
	fragmentDocument := document
	if kind, _ := mappingScalar(root, "kind"); kind == "ConfigMap" {
		target.configMap = true
		if dataKey == "" {
			return retirementTarget{}, errors.New("ConfigMap target requires --data-key; the evaluator fragment_path is only a locator")
		}
		data, ok := mappingValue(root, "data")
		if !ok || data.Kind != yaml.MappingNode {
			return retirementTarget{}, errors.New("ConfigMap target has no mapping data field")
		}
		entry, ok := mappingValue(data, dataKey)
		if !ok || entry.Kind != yaml.ScalarNode {
			return retirementTarget{}, fmt.Errorf("ConfigMap data key %q is missing or not a string", dataKey)
		}
		fragmentDocument, err = parseYAMLDocument([]byte(entry.Value))
		if err != nil {
			return retirementTarget{}, fmt.Errorf("parse ConfigMap data[%q]: %w", dataKey, err)
		}
		fragmentRoot = yamlDocumentRoot(fragmentDocument)
		if fragmentRoot == nil || fragmentRoot.Kind != yaml.MappingNode {
			return retirementTarget{}, fmt.Errorf("ConfigMap data[%q] does not contain a fragment mapping", dataKey)
		}
	} else if dataKey != "" {
		return retirementTarget{}, errors.New("--data-key is only valid when --target is a ConfigMap")
	}

	if err := addRetirementBlock(fragmentRoot, block); err != nil {
		return retirementTarget{}, err
	}
	target.fragment, err = renderYAMLDocument(fragmentDocument)
	if err != nil {
		return retirementTarget{}, err
	}
	if target.configMap {
		data, _ := mappingValue(root, "data")
		entry, _ := mappingValue(data, dataKey)
		entry.Value = strings.TrimSuffix(string(target.fragment), "\n")
		entry.Style = yaml.LiteralStyle
	}
	target.rendered, err = renderYAMLDocument(document)
	if err != nil {
		return retirementTarget{}, err
	}
	target.owner, _ = mappingScalar(fragmentRoot, "x-seam-owner")
	return target, nil
}

func addRetirementBlock(root *yaml.Node, raw string) error {
	if _, exists := mappingValue(root, "x-seam-deprecated"); exists {
		return errors.New("target fragment already contains x-seam-deprecated; refusing to overwrite it")
	}
	blockDocument, err := parseYAMLDocument([]byte(raw))
	if err != nil {
		return fmt.Errorf("parse proposal block: %w", err)
	}
	blockRoot := yamlDocumentRoot(blockDocument)
	value, ok := mappingValue(blockRoot, "x-seam-deprecated")
	if !ok {
		return errors.New("proposal block has no x-seam-deprecated value")
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "x-seam-deprecated"},
		value,
	)
	return nil
}

func parseYAMLDocument(contents []byte) (*yaml.Node, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return nil, err
	}
	return &document, nil
}

func yamlDocumentRoot(document *yaml.Node) *yaml.Node {
	if document == nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil
	}
	return document.Content[0]
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, bool) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1], true
		}
	}
	return nil, false
}

func mappingScalar(mapping *yaml.Node, key string) (string, bool) {
	value, ok := mappingValue(mapping, key)
	if !ok || value.Kind != yaml.ScalarNode {
		return "", false
	}
	return value.Value, true
}

func renderYAMLDocument(document *yaml.Node) ([]byte, error) {
	if yamlDocumentRoot(document) == nil {
		return nil, errors.New("YAML document has no root")
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	if err := encoder.Encode(document.Content[0]); err != nil {
		_ = encoder.Close()
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func lintRetirementFragment(fragment []byte, owner, schemaPath, allowlistPath string) (spec.LintReport, error) {
	temporaryRoot, err := os.MkdirTemp("", "seam-retirement-handoff-")
	if err != nil {
		return spec.LintReport{}, err
	}
	defer func() { _ = os.RemoveAll(temporaryRoot) }()
	if owner == "" {
		owner = "retirement-handoff"
	}
	fragmentDir := filepath.Join(temporaryRoot, owner)
	if err := os.MkdirAll(fragmentDir, 0o755); err != nil {
		return spec.LintReport{}, err
	}
	fragmentPath := filepath.Join(fragmentDir, "fragment.yaml")
	if err := os.WriteFile(fragmentPath, fragment, 0o644); err != nil {
		return spec.LintReport{}, err
	}
	return spec.LintFiles([]string{fragmentPath}, spec.LintOptions{
		SchemaPath:            schemaPath,
		UpstreamAllowlistPath: allowlistPath,
	})
}

func writeRetirementTarget(target retirementTarget) error {
	info, err := os.Stat(target.path)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target.path), ".seam-retirement-handoff-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(target.rendered); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, target.path)
}

func readHotReloadStatus(client *http.Client, endpoint string) (hotReloadStatus, error) {
	response, err := client.Get(endpoint)
	if err != nil {
		return hotReloadStatus{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return hotReloadStatus{}, fmt.Errorf("GET %s returned HTTP %d", endpoint, response.StatusCode)
	}
	var payload retirementHealthResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return hotReloadStatus{}, fmt.Errorf("decode %s: %w", endpoint, err)
	}
	status := payload.HotReload
	if status == nil {
		status = payload.RouteTable.HotReload
	}
	if status == nil {
		return hotReloadStatus{}, errors.New("response has no hot_reload status; use /health/upstreams or /config/status")
	}
	if !status.Enabled {
		return hotReloadStatus{}, errors.New("hot reload is disabled; enable SEAM hot reload before applying a route change")
	}
	return *status, nil
}

func waitForRetirementHotReload(client *http.Client, endpoint string, before hotReloadStatus, timeout, interval time.Duration) (hotReloadStatus, error) {
	deadline := time.Now().Add(timeout)
	for {
		status, err := readHotReloadStatus(client, endpoint)
		if err != nil {
			return hotReloadStatus{}, err
		}
		if status.FailureCount > before.FailureCount {
			return hotReloadStatus{}, fmt.Errorf("reload failure count increased from %d to %d", before.FailureCount, status.FailureCount)
		}
		if status.ReloadCount > before.ReloadCount {
			return status, nil
		}
		if time.Now().After(deadline) {
			return hotReloadStatus{}, fmt.Errorf("reload count stayed at %d for %s", before.ReloadCount, timeout)
		}
		time.Sleep(interval)
	}
}
