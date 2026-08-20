package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ardenone/seam/tools/diffharness/internal/compare"
	"github.com/ardenone/seam/tools/diffharness/internal/corpus"
)

type lifecycleExchange struct {
	name                string
	method              string
	target              string
	requestBody         []byte
	requestContentType  string
	responseStatus      int
	responseBody        []byte
	responseContentType string
}

// TestCorpusLifecycleWithRealisticWorkload is the final integration layer for
// the differential corpus. It drives the real capture executable through a
// mixed workload, gracefully persists its output, loads and queries that
// output, analyzes it with the replay engine, and finally exercises the
// archive/cleanup workflow used after a cutover.
func TestCorpusLifecycleWithRealisticWorkload(t *testing.T) {
	exchanges := []lifecycleExchange{
		{
			name:                "first-list-page",
			method:              http.MethodGet,
			target:              "/v1/resources?limit=2&cursor=first",
			responseStatus:      http.StatusOK,
			responseBody:        []byte(`{"items":["alpha","beta"],"next":"second"}`),
			responseContentType: "application/json",
		},
		{
			name:                "second-list-page",
			method:              http.MethodGet,
			target:              "/v1/resources?limit=2&cursor=second",
			responseStatus:      http.StatusOK,
			responseBody:        []byte(`{"items":["gamma"],"next":null}`),
			responseContentType: "application/json",
		},
		{
			name:                "create-resource",
			method:              http.MethodPost,
			target:              "/v1/resources?dryRun=false",
			requestBody:         []byte(`{"name":"delta","labels":{"team":"platform"}}`),
			requestContentType:  "application/json",
			responseStatus:      http.StatusCreated,
			responseBody:        []byte(`{"name":"delta","revision":1}`),
			responseContentType: "application/json",
		},
		{
			name:                "replace-binary-attachment",
			method:              http.MethodPut,
			target:              "/v1/resources/delta/attachment",
			requestBody:         []byte{0x00, 0x01, 0xfe, 0xff, 0x7f},
			requestContentType:  "application/octet-stream",
			responseStatus:      http.StatusAccepted,
			responseBody:        []byte{0xca, 0xfe, 0xba, 0xbe},
			responseContentType: "application/octet-stream",
		},
		{
			name:           "delete-resource",
			method:         http.MethodDelete,
			target:         "/v1/resources/delta",
			responseStatus: http.StatusNoContent,
		},
	}

	upstreamHandler := realisticWorkloadHandler(t, exchanges)
	incumbent := httptest.NewServer(upstreamHandler)
	defer incumbent.Close()
	seam := httptest.NewServer(upstreamHandler)
	defer seam.Close()

	tempDir := t.TempDir()
	corpusPath := filepath.Join(tempDir, "active", "corpus.json")
	if err := os.MkdirAll(filepath.Dir(corpusPath), 0o755); err != nil {
		t.Fatalf("create active corpus directory: %v", err)
	}

	captureBinary := buildCaptureBinary(t)
	captureAddress := unusedLoopbackAddress(t)
	captureURL := "http://" + captureAddress
	captureCommand := exec.Command(
		captureBinary,
		"--incumbent", incumbent.URL,
		"--service", "lifecycle-integration",
		"--corpus", corpusPath,
		"--listen", captureAddress,
		"--description", "Realistic mixed-method lifecycle corpus",
		"--capture-enabled=true",
	)
	var captureLogs bytes.Buffer
	captureCommand.Stdout = &captureLogs
	captureCommand.Stderr = &captureLogs
	if err := captureCommand.Start(); err != nil {
		t.Fatalf("start seam-capture: %v", err)
	}
	captureRunning := true
	t.Cleanup(func() {
		if captureRunning {
			_ = captureCommand.Process.Kill()
			_ = captureCommand.Wait()
		}
	})
	waitForCaptureListener(t, captureAddress)

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableCompression: true,
		},
	}
	for _, exchange := range exchanges {
		runLifecycleExchange(t, client, captureURL, exchange)
	}

	// Graceful termination is the persistence boundary of seam-capture.
	if err := captureCommand.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt seam-capture: %v", err)
	}
	waitResult := make(chan error, 1)
	go func() { waitResult <- captureCommand.Wait() }()
	select {
	case err := <-waitResult:
		captureRunning = false
		if err != nil {
			t.Fatalf("seam-capture exited with an error: %v\n%s", err, captureLogs.String())
		}
	case <-time.After(10 * time.Second):
		_ = captureCommand.Process.Kill()
		<-waitResult
		captureRunning = false
		t.Fatalf("seam-capture did not shut down gracefully\n%s", captureLogs.String())
	}

	loaded, err := corpus.Load(corpusPath)
	if err != nil {
		t.Fatalf("load persisted corpus: %v", err)
	}
	verifyCompleteCorpus(t, loaded, incumbent.URL, exchanges)
	verifyMinimalCorpusJSON(t, corpusPath, loaded, exchanges)

	// Exercise corpus queries that analysis and cutover tooling rely on. The
	// two requests with the same method/path remain independently queryable by
	// their distinct raw query strings, while mutation analysis spans methods,
	// statuses, and binary/JSON payloads.
	listEntries := queryCorpusEntries(loaded, func(entry corpus.Entry) bool {
		return entry.Request.Method == http.MethodGet && entry.Request.Path == "/v1/resources"
	})
	if len(listEntries) != 2 {
		t.Fatalf("list query returned %d entries, want 2", len(listEntries))
	}
	gotQueries := []string{listEntries[0].Request.Query, listEntries[1].Request.Query}
	sort.Strings(gotQueries)
	wantQueries := []string{"limit=2&cursor=first", "limit=2&cursor=second"}
	sort.Strings(wantQueries)
	if !reflect.DeepEqual(gotQueries, wantQueries) {
		t.Fatalf("list query strings = %v, want %v", gotQueries, wantQueries)
	}

	analysis := analyzeCorpus(loaded)
	wantMethods := map[string]int{
		http.MethodGet:    2,
		http.MethodPost:   1,
		http.MethodPut:    1,
		http.MethodDelete: 1,
	}
	if !reflect.DeepEqual(analysis.MethodCounts, wantMethods) {
		t.Fatalf("method analysis = %v, want %v", analysis.MethodCounts, wantMethods)
	}
	if analysis.StatusCounts[http.StatusOK] != 2 ||
		analysis.StatusCounts[http.StatusCreated] != 1 ||
		analysis.StatusCounts[http.StatusAccepted] != 1 ||
		analysis.StatusCounts[http.StatusNoContent] != 1 {
		t.Fatalf("status analysis is incomplete: %v", analysis.StatusCounts)
	}
	if analysis.RequestBodyBytes == 0 || analysis.ResponseBodyBytes == 0 {
		t.Fatalf("body-size analysis did not observe captured payloads: %+v", analysis)
	}

	// Run the actual differential analysis engine and persist its report. This
	// proves the server-produced corpus is directly consumable by seam-replay,
	// rather than merely being valid JSON.
	replay := &replayer{
		incumbentURL:  incumbent.URL,
		seamURL:       seam.URL,
		corpusPath:    corpusPath,
		cp:            loaded,
		ignoreHeaders: []string{"Date", "Server"},
	}
	report := replay.run()
	if report.PassCount != len(exchanges) || report.FailCount != 0 || report.SkipCount != 0 {
		t.Fatalf("unexpected replay analysis: pass=%d fail=%d skip=%d entries=%+v",
			report.PassCount, report.FailCount, report.SkipCount, report.Entries)
	}
	for _, entry := range report.Entries {
		if entry.Verdict != compare.VerdictPass {
			t.Fatalf("replay entry %q verdict = %s, want PASS", entry.ID, entry.Verdict)
		}
	}
	reportPath := filepath.Join(tempDir, "analysis", "report.json")
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
		t.Fatalf("create analysis directory: %v", err)
	}
	if err := writeReport(report, reportPath); err != nil {
		t.Fatalf("persist replay analysis: %v", err)
	}
	var persistedReport Report
	reportBytes, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read replay analysis: %v", err)
	}
	if err := json.Unmarshal(reportBytes, &persistedReport); err != nil {
		t.Fatalf("parse replay analysis: %v", err)
	}
	if persistedReport.PassCount != len(exchanges) || persistedReport.Corpus != loaded.Service {
		t.Fatalf("persisted replay analysis is incomplete: %+v", persistedReport)
	}
	var summary strings.Builder
	printSummary(&persistedReport, &summary)
	if !strings.Contains(summary.String(), "All entries passed") {
		t.Fatalf("human-readable analysis omitted success summary:\n%s", summary.String())
	}

	// Archive the completed active corpus, prove it still loads from the
	// archive, then clean it up. All paths live under t.TempDir so this test can
	// never affect a checked-in corpus.
	archiveDir := filepath.Join(tempDir, "archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("create corpus archive: %v", err)
	}
	archivePath := filepath.Join(archiveDir, "lifecycle-integration.json")
	if err := os.Rename(corpusPath, archivePath); err != nil {
		t.Fatalf("archive corpus: %v", err)
	}
	if _, err := os.Stat(corpusPath); !os.IsNotExist(err) {
		t.Fatalf("active corpus still exists after archival: %v", err)
	}
	archived, err := corpus.Load(archivePath)
	if err != nil {
		t.Fatalf("load archived corpus: %v", err)
	}
	if len(archived.Entries) != len(exchanges) {
		t.Fatalf("archived corpus entries = %d, want %d", len(archived.Entries), len(exchanges))
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("clean up archived corpus: %v", err)
	}
	remaining, err := os.ReadDir(archiveDir)
	if err != nil {
		t.Fatalf("read cleaned archive directory: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("archive cleanup left %d entries", len(remaining))
	}
}

func realisticWorkloadHandler(t *testing.T, exchanges []lifecycleExchange) http.Handler {
	t.Helper()
	byRequest := make(map[string]lifecycleExchange, len(exchanges))
	for _, exchange := range exchanges {
		byRequest[exchange.method+" "+exchange.target] = exchange
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.RequestURI()
		exchange, ok := byRequest[key]
		if !ok {
			http.Error(w, "unexpected realistic workload request", http.StatusNotFound)
			t.Errorf("unexpected workload request %q", key)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request body", http.StatusInternalServerError)
			t.Errorf("read %s request body: %v", exchange.name, err)
			return
		}
		if !bytes.Equal(body, exchange.requestBody) {
			http.Error(w, "request body mismatch", http.StatusBadRequest)
			t.Errorf("%s request body = %v, want %v", exchange.name, body, exchange.requestBody)
			return
		}
		if got := r.Header.Get("X-Workload-Step"); got != exchange.name {
			http.Error(w, "workload header mismatch", http.StatusBadRequest)
			t.Errorf("%s workload header = %q", exchange.name, got)
			return
		}

		w.Header().Set("X-Upstream-Route", exchange.name)
		if exchange.responseContentType != "" {
			w.Header().Set("Content-Type", exchange.responseContentType)
		}
		w.WriteHeader(exchange.responseStatus)
		if len(exchange.responseBody) > 0 {
			_, _ = w.Write(exchange.responseBody)
		}
	})
}

func runLifecycleExchange(t *testing.T, client *http.Client, captureURL string, exchange lifecycleExchange) {
	t.Helper()
	req, err := http.NewRequest(exchange.method, captureURL+exchange.target, bytes.NewReader(exchange.requestBody))
	if err != nil {
		t.Fatalf("create %s request: %v", exchange.name, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Workload-Step", exchange.name)
	if exchange.requestContentType != "" {
		req.Header.Set("Content-Type", exchange.requestContentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("execute %s request: %v", exchange.name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s response: %v", exchange.name, err)
	}
	if resp.StatusCode != exchange.responseStatus || !bytes.Equal(body, exchange.responseBody) {
		t.Fatalf("%s response = status %d body %v, want status %d body %v",
			exchange.name, resp.StatusCode, body, exchange.responseStatus, exchange.responseBody)
	}
}

func buildCaptureBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "seam-capture")
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "../seam-capture")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build seam-capture: %v\n%s", err, output)
	}
	return binary
}

func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve capture address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release capture address: %v", err)
	}
	return address
}

func waitForCaptureListener(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("capture listener %s did not become ready", address)
}

func verifyCompleteCorpus(
	t *testing.T,
	loaded *corpus.Corpus,
	incumbentURL string,
	exchanges []lifecycleExchange,
) {
	t.Helper()
	if loaded.Schema != corpus.SchemaVersion || loaded.Service != "lifecycle-integration" ||
		loaded.Incumbent != incumbentURL || loaded.Description == "" {
		t.Fatalf("corpus metadata is incomplete: %+v", loaded)
	}
	if _, err := time.Parse(time.RFC3339Nano, loaded.CapturedAt); err != nil {
		t.Fatalf("corpus capturedAt is invalid: %v", err)
	}
	if len(loaded.Entries) != len(exchanges) {
		t.Fatalf("captured entries = %d, want %d", len(loaded.Entries), len(exchanges))
	}

	wantByRequest := make(map[string]lifecycleExchange, len(exchanges))
	for _, exchange := range exchanges {
		wantByRequest[exchange.method+" "+exchange.target] = exchange
	}
	seenIDs := make(map[string]struct{}, len(loaded.Entries))
	for _, entry := range loaded.Entries {
		if entry.ID == "" || entry.Timestamp == "" || entry.Description == "" {
			t.Fatalf("entry metadata is incomplete: %+v", entry)
		}
		if _, duplicate := seenIDs[entry.ID]; duplicate {
			t.Fatalf("duplicate entry ID %q", entry.ID)
		}
		seenIDs[entry.ID] = struct{}{}
		if _, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err != nil {
			t.Fatalf("entry %q timestamp is invalid: %v", entry.ID, err)
		}

		target := entry.Request.Path
		if entry.Request.Query != "" {
			target += "?" + entry.Request.Query
		}
		exchange, ok := wantByRequest[entry.Request.Method+" "+target]
		if !ok {
			t.Fatalf("captured unexpected request %s %s", entry.Request.Method, target)
		}
		if got := entry.Request.Headers["X-Workload-Step"]; !reflect.DeepEqual(got, []string{exchange.name}) {
			t.Fatalf("entry %q workload header = %v, want %q", entry.ID, got, exchange.name)
		}
		if entry.Request.BodyContentType != exchange.requestContentType {
			t.Fatalf("entry %q request content type = %q, want %q",
				entry.ID, entry.Request.BodyContentType, exchange.requestContentType)
		}
		assertEncodedBody(t, entry.ID+" request", entry.Request.BodyB64, exchange.requestBody)

		if entry.Response == nil {
			t.Fatalf("entry %q has no captured response", entry.ID)
		}
		if entry.Response.StatusCode != exchange.responseStatus ||
			entry.Response.BodyContentType != exchange.responseContentType {
			t.Fatalf("entry %q response metadata = %+v", entry.ID, entry.Response)
		}
		if got := entry.Response.Headers["X-Upstream-Route"]; !reflect.DeepEqual(got, []string{exchange.name}) {
			t.Fatalf("entry %q upstream header = %v, want %q", entry.ID, got, exchange.name)
		}
		assertEncodedBody(t, entry.ID+" response", entry.Response.BodyB64, exchange.responseBody)
	}
}

func verifyMinimalCorpusJSON(
	t *testing.T,
	path string,
	loaded *corpus.Corpus,
	exchanges []lifecycleExchange,
) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corpus for minimality check: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse corpus for minimality check: %v", err)
	}
	assertOnlyKeys(t, "corpus", document, "schema", "service", "incumbent", "capturedAt", "description", "entries")

	rawEntries, ok := document["entries"].([]any)
	if !ok {
		t.Fatalf("serialized entries has type %T", document["entries"])
	}
	if len(rawEntries) != len(exchanges) || len(loaded.Entries) != len(exchanges) {
		t.Fatalf("serialized corpus contains redundant or missing entries: %d", len(rawEntries))
	}
	semanticRequests := make(map[string]struct{}, len(rawEntries))
	for index, item := range rawEntries {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("entry %d has type %T", index, item)
		}
		assertOnlyKeys(t, fmt.Sprintf("entry %d", index), entry,
			"id", "timestamp", "description", "request", "response")

		request, ok := entry["request"].(map[string]any)
		if !ok {
			t.Fatalf("entry %d request has type %T", index, entry["request"])
		}
		assertAllowedKeys(t, fmt.Sprintf("entry %d request", index), request,
			"method", "path", "query", "headers", "bodyB64", "bodyContentType")
		method, _ := request["method"].(string)
		requestPath, _ := request["path"].(string)
		query, hasQuery := request["query"].(string)
		bodyB64, hasBody := request["bodyB64"].(string)
		contentType, hasContentType := request["bodyContentType"].(string)
		if query == "" && hasQuery {
			t.Fatalf("entry %d serializes an empty query", index)
		}
		if bodyB64 == "" && hasBody {
			t.Fatalf("entry %d serializes an empty request body", index)
		}
		if contentType == "" && hasContentType {
			t.Fatalf("entry %d serializes an empty request content type", index)
		}
		semanticKey := strings.Join([]string{method, requestPath, query, bodyB64}, "\x00")
		if _, duplicate := semanticRequests[semanticKey]; duplicate {
			t.Fatalf("entry %d redundantly captures request %s %s?%s", index, method, requestPath, query)
		}
		semanticRequests[semanticKey] = struct{}{}

		response, ok := entry["response"].(map[string]any)
		if !ok {
			t.Fatalf("entry %d response has type %T", index, entry["response"])
		}
		assertAllowedKeys(t, fmt.Sprintf("entry %d response", index), response,
			"statusCode", "headers", "bodyB64", "bodyContentType")
		if body, present := response["bodyB64"].(string); present && body == "" {
			t.Fatalf("entry %d serializes an empty response body", index)
		}
		if responseType, present := response["bodyContentType"].(string); present && responseType == "" {
			t.Fatalf("entry %d serializes an empty response content type", index)
		}
	}
}

func queryCorpusEntries(cp *corpus.Corpus, matches func(corpus.Entry) bool) []corpus.Entry {
	entries := make([]corpus.Entry, 0)
	for _, entry := range cp.Entries {
		if matches(entry) {
			entries = append(entries, entry)
		}
	}
	return entries
}

type corpusAnalysis struct {
	MethodCounts      map[string]int
	StatusCounts      map[int]int
	RequestBodyBytes  int
	ResponseBodyBytes int
}

func analyzeCorpus(cp *corpus.Corpus) corpusAnalysis {
	analysis := corpusAnalysis{
		MethodCounts: make(map[string]int),
		StatusCounts: make(map[int]int),
	}
	for _, entry := range cp.Entries {
		analysis.MethodCounts[entry.Request.Method]++
		requestBody, _ := base64.StdEncoding.DecodeString(entry.Request.BodyB64)
		analysis.RequestBodyBytes += len(requestBody)
		if entry.Response != nil {
			analysis.StatusCounts[entry.Response.StatusCode]++
			responseBody, _ := base64.StdEncoding.DecodeString(entry.Response.BodyB64)
			analysis.ResponseBodyBytes += len(responseBody)
		}
	}
	return analysis
}

func assertEncodedBody(t *testing.T, name, encoded string, want []byte) {
	t.Helper()
	got, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode %s body: %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s body = %v, want %v", name, got, want)
	}
}

func assertOnlyKeys(t *testing.T, name string, value map[string]any, keys ...string) {
	t.Helper()
	assertAllowedKeys(t, name, value, keys...)
	if len(value) != len(keys) {
		t.Fatalf("%s fields = %v, want exactly %v", name, sortedKeys(value), keys)
	}
}

func assertAllowedKeys(t *testing.T, name string, value map[string]any, keys ...string) {
	t.Helper()
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
	}
	for key := range value {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("%s contains unnecessary field %q", name, key)
		}
	}
}

func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
