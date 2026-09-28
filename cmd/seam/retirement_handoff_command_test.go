package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const handoffTestBlock = `x-seam-deprecated:
  since: "2026-09-26"
  sunset: "2026-12-25"
  brownout:
    - start: "2026-10-26T11:35:23Z"
      end: "2026-11-02T11:35:23Z"
    - start: "2026-11-25T11:35:23Z"
      end: "2026-12-02T11:35:23Z"
    - start: "2026-12-18T00:00:00Z"
      end: "2026-12-25T00:00:00Z"
`

const handoffTestFragment = `x-seam-schema: v1
x-seam-owner: legacy-service
x-api-version: v1
x-upstream: https://legacy-service.example.internal
openapi: 3.1.0
info:
  title: legacy-service
  version: "1.0.0"
paths:
  /old-route:
    get:
      responses:
        "200":
          description: ok
`

func writeHandoffFinding(t *testing.T, dir, block string) string {
	t.Helper()
	record, err := json.Marshal(map[string]string{
		"level":                   "info",
		"msg":                     retirementFindingMessage,
		"route":                   "/old-route",
		"api_version":             "v1",
		"spec_version":            "spec-123",
		"fragment_path":           "k8s/rs-manager/seam/routes.d/legacy-service/fragment.yaml",
		"x_seam_deprecated_block": block,
	})
	if err != nil {
		t.Fatalf("marshal finding: %v", err)
	}
	path := filepath.Join(dir, "finding.jsonl")
	if err := os.WriteFile(path, append(record, '\n'), 0o644); err != nil {
		t.Fatalf("write finding: %v", err)
	}
	return path
}

func writeHandoffFragment(t *testing.T, dir string) string {
	t.Helper()
	ownerDir := filepath.Join(dir, "legacy-service")
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		t.Fatalf("mkdir owner: %v", err)
	}
	path := filepath.Join(ownerDir, "fragment.yaml")
	if err := os.WriteFile(path, []byte(handoffTestFragment), 0o644); err != nil {
		t.Fatalf("write fragment: %v", err)
	}
	return path
}

func handoffSchemaPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "spec", "route-fragment-schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func runHandoff(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runRetirementHandoffCommand(args, strings.NewReader(""), &stdout, &stderr, http.DefaultClient)
	return code, stdout.String(), stderr.String()
}

func TestRetirementHandoffDryRunLintsAndDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	finding := writeHandoffFinding(t, dir, handoffTestBlock)
	fragment := writeHandoffFragment(t, dir)
	before, err := os.ReadFile(fragment)
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runHandoff(t,
		"--finding", finding,
		"--target", fragment,
		"--schema", handoffSchemaPath(t),
	)
	if code != 0 {
		t.Fatalf("dry-run exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	after, err := os.ReadFile(fragment)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("dry-run changed the fragment")
	}
	for _, want := range []string{"plan:", "lint: passed", "was not written"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRetirementHandoffAppliesConfigMapRootAndPrintsRevert(t *testing.T) {
	dir := t.TempDir()
	finding := writeHandoffFinding(t, dir, handoffTestBlock)
	manifest := filepath.Join(dir, "configmap-routes-legacy.yaml")
	contents := `apiVersion: v1
kind: ConfigMap
metadata:
  name: seam-routes-legacy-service
data:
  legacy.yaml: |
    ` + strings.ReplaceAll(strings.TrimSuffix(handoffTestFragment, "\n"), "\n", "\n    ") + "\n"
	if err := os.WriteFile(manifest, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runHandoff(t,
		"--finding", finding,
		"--target", manifest,
		"--data-key", "legacy.yaml",
		"--schema", handoffSchemaPath(t),
		"--apply",
	)
	if code != 0 {
		t.Fatalf("apply exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	document, err := parseYAMLDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	data, ok := mappingValue(yamlDocumentRoot(document), "data")
	if !ok {
		t.Fatal("manifest data missing")
	}
	entry, ok := mappingValue(data, "legacy.yaml")
	if !ok {
		t.Fatal("manifest data key missing")
	}
	fragment, err := parseYAMLDocument([]byte(entry.Value))
	if err != nil {
		t.Fatalf("updated fragment is not YAML: %v", err)
	}
	fragmentRoot := yamlDocumentRoot(fragment)
	if _, ok := mappingValue(fragmentRoot, "x-seam-deprecated"); !ok {
		t.Fatal("proposal was not inserted at fragment root")
	}
	if _, ok := mappingValue(yamlDocumentRoot(fragment), "paths"); !ok {
		t.Fatal("updated fragment lost paths")
	}
	if !strings.Contains(stdout, "git revert <landing-commit>") {
		t.Errorf("stdout omitted reversible handoff guidance:\n%s", stdout)
	}
}

func TestRetirementHandoffRejectsLintFailureWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	invalidBlock := strings.Replace(handoffTestBlock, "brownout:", "brownouts:", 1)
	finding := writeHandoffFinding(t, dir, invalidBlock)
	fragment := writeHandoffFragment(t, dir)
	before, err := os.ReadFile(fragment)
	if err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runHandoff(t,
		"--finding", finding,
		"--target", fragment,
		"--schema", handoffSchemaPath(t),
		"--apply",
	)
	if code != 1 {
		t.Fatalf("invalid proposal exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "pre-land lint") || !strings.Contains(stderr, "deprecation.unknown-field") {
		t.Fatalf("invalid proposal did not identify the lint gate:\n%s", stderr)
	}
	after, err := os.ReadFile(fragment)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("lint failure changed the fragment")
	}
}

func TestRetirementHandoffRefusesToOverwriteExistingMarker(t *testing.T) {
	dir := t.TempDir()
	finding := writeHandoffFinding(t, dir, handoffTestBlock)
	fragment := writeHandoffFragment(t, dir)
	if err := os.WriteFile(fragment, append([]byte(handoffTestBlock), []byte(handoffTestFragment)...), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runHandoff(t,
		"--finding", finding,
		"--target", fragment,
		"--schema", handoffSchemaPath(t),
		"--apply",
	)
	if code != 1 || !strings.Contains(stderr, "refusing to overwrite") {
		t.Fatalf("existing-marker safety check exit=%d stderr=%s", code, stderr)
	}
}

func TestRetirementHandoffObservesSuccessfulHotReload(t *testing.T) {
	dir := t.TempDir()
	finding := writeHandoffFinding(t, dir, handoffTestBlock)
	fragment := writeHandoffFragment(t, dir)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := calls.Add(1)
		reloadCount := uint64(0)
		if count > 1 {
			reloadCount = 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"route_table":{"hot_reload":{"enabled":true,"reload_count":%d,"failure_count":0,"last_reload_time":"2026-09-28T12:00:00Z"}}}`, reloadCount)
	}))
	defer server.Close()

	code, stdout, stderr := runHandoff(t,
		"--finding", finding,
		"--target", fragment,
		"--schema", handoffSchemaPath(t),
		"--apply",
	)
	if code != 0 {
		t.Fatalf("observe exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	code, stdout, stderr = runHandoff(t,
		"--observe-only",
		"--observe-url", server.URL,
		"--observe-timeout", "200ms",
		"--observe-interval", "1ms",
	)
	if code != 0 {
		t.Fatalf("observe-only exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "hot reload observed: reload_count=1") {
		t.Fatalf("stdout missing reload observation:\n%s", stdout)
	}
}

func TestRetirementHandoffRequiresExplicitTargetForLocator(t *testing.T) {
	dir := t.TempDir()
	finding := writeHandoffFinding(t, dir, handoffTestBlock)
	fragment := writeHandoffFragment(t, dir)
	code, _, stderr := runHandoff(t,
		"--finding", finding,
		"--target", fragment,
		"--data-key", "fragment.yaml",
		"--schema", handoffSchemaPath(t),
	)
	if code != 1 || !strings.Contains(stderr, "only valid when --target is a ConfigMap") {
		t.Fatalf("data-key safety check exit=%d stderr=%s", code, stderr)
	}
}

func TestRetirementHandoffObserverRejectsDisabledHotReload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"hot_reload":{"enabled":false,"reload_count":0,"failure_count":0}}`)
	}))
	defer server.Close()
	_, err := readHotReloadStatus(server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "hot reload is disabled") {
		t.Fatalf("disabled observer error = %v", err)
	}
}

func TestRetirementHandoffObservationTimeoutIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"hot_reload":{"enabled":true,"reload_count":4,"failure_count":0}}`)
	}))
	defer server.Close()
	start := time.Now()
	_, err := waitForRetirementHotReload(server.Client(), server.URL, hotReloadStatus{Enabled: true, ReloadCount: 4}, 10*time.Millisecond, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "reload count stayed") {
		t.Fatalf("timeout error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("bounded observation took %s", elapsed)
	}
}
