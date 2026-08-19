package baseline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Baseline represents a stored benchmark baseline
type Baseline struct {
	Timestamp string                     `json:"timestamp"`
	CommitSHA string                     `json:"commit_sha"`
	CommitMsg string                     `json:"commit_message,omitempty"`
	Metrics   map[string]BenchmarkMetric `json:"metrics"`
	Version   string                     `json:"version"` // Format version
}

// BenchmarkMetric represents metrics for a single benchmark
type BenchmarkMetric struct {
	Name          string             `json:"name"`
	NsPerOp       float64            `json:"ns_per_op"`
	MbPerSec      float64            `json:"mb_per_sec,omitempty"`
	BytesPerOp    uint64             `json:"bytes_per_op"`
	AllocsPerOp   uint64             `json:"allocs_per_op"`
	CustomMetrics map[string]float64 `json:"custom_metrics,omitempty"`
}

// RegressionResult represents the comparison between current and baseline
type RegressionResult struct {
	BenchmarkName string  `json:"benchmark_name"`
	MetricName    string  `json:"metric_name"`
	BaselineValue float64 `json:"baseline_value"`
	CurrentValue  float64 `json:"current_value"`
	PercentChange float64 `json:"percent_change"`
	IsRegression  bool    `json:"is_regression"`
	Threshold     float64 `json:"threshold"`
}

// ComparisonReport contains regression analysis results
type ComparisonReport struct {
	Timestamp           string             `json:"timestamp"`
	CurrentCommitSHA    string             `json:"current_commit_sha"`
	BaselineCommitSHA   string             `json:"baseline_commit_sha"`
	BaselineTimestamp   string             `json:"baseline_timestamp"`
	Regressions         []RegressionResult `json:"regressions"`
	Improvements        []RegressionResult `json:"improvements"`
	Warnings            []RegressionResult `json:"warnings"`
	Passed              bool               `json:"passed"`
	RegressionThreshold float64            `json:"regression_threshold"`
}

const (
	// BaselineDir is where baseline files are stored
	BaselineDir = "benchmarks/baselines"

	// Current format version
	FormatVersion = "v1"

	// Default regression threshold (10%)
	DefaultThreshold = 10.0

	// Warning threshold (5%)
	WarningThreshold = 5.0
)

// BenchmarkFiles maps benchmark categories to baseline file names.
var BenchmarkFiles = map[string]string{
	"openbao":    "openbao-latency-baseline.json",
	"memory":     "memory-concurrency-baseline.json",
	"throughput": "throughput-baseline.json",
}

// ValidateBenchmarkType reports whether benchmarkType has a configured
// baseline file.
func ValidateBenchmarkType(benchmarkType string) error {
	if _, ok := BenchmarkFiles[benchmarkType]; !ok {
		return fmt.Errorf("unknown benchmark type: %s", benchmarkType)
	}
	return nil
}

// getCurrentCommit gets the current git commit SHA and message
func getCurrentCommit() (string, string, error) {
	shaBytes, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", "", fmt.Errorf("failed to get commit SHA: %w", err)
	}
	sha := strings.TrimSpace(string(shaBytes))

	msgBytes, err := exec.Command("git", "log", "-1", "--pretty=%B").Output()
	if err != nil {
		return "", "", fmt.Errorf("failed to get commit message: %w", err)
	}
	msg := strings.TrimSpace(string(msgBytes))

	return sha, msg, nil
}

// LoadBaseline loads a baseline from file
func LoadBaseline(benchmarkType string) (*Baseline, error) {
	if err := ValidateBenchmarkType(benchmarkType); err != nil {
		return nil, err
	}
	filename := BenchmarkFiles[benchmarkType]

	path := filepath.Join(BaselineDir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read baseline file %s: %w", path, err)
	}

	var baseline Baseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		return nil, fmt.Errorf("failed to parse baseline JSON: %w", err)
	}

	return &baseline, nil
}

// SaveBaseline saves a baseline to file
func SaveBaseline(benchmarkType string, metrics map[string]BenchmarkMetric) error {
	if err := ValidateBenchmarkType(benchmarkType); err != nil {
		return err
	}

	sha, msg, err := getCurrentCommit()
	if err != nil {
		return fmt.Errorf("failed to get current commit: %w", err)
	}

	baseline := Baseline{
		Timestamp: time.Now().Format(time.RFC3339),
		CommitSHA: sha,
		CommitMsg: msg,
		Metrics:   metrics,
		Version:   FormatVersion,
	}

	filename := BenchmarkFiles[benchmarkType]

	// Ensure directory exists
	if err := os.MkdirAll(BaselineDir, 0755); err != nil {
		return fmt.Errorf("failed to create baseline directory: %w", err)
	}

	path := filepath.Join(BaselineDir, filename)
	data, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal baseline JSON: %w", err)
	}

	// Write and rename so an interrupted capture never leaves a truncated
	// baseline that could make the next CI run fail to parse.
	tmp, err := os.CreateTemp(BaselineDir, ".baseline-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary baseline file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to set baseline file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write baseline file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close baseline file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed to install baseline file: %w", err)
	}

	return nil
}

// CompareWithBaseline compares current metrics against baseline
func CompareWithBaseline(benchmarkType string, currentMetrics map[string]BenchmarkMetric, threshold float64) (*ComparisonReport, error) {
	baseline, err := LoadBaseline(benchmarkType)
	if err != nil {
		return nil, fmt.Errorf("failed to load baseline: %w", err)
	}

	sha, _, err := getCurrentCommit()
	if err != nil {
		return nil, fmt.Errorf("failed to get current commit: %w", err)
	}

	return compareMetrics(baseline, currentMetrics, threshold, sha)
}

// CompareMetrics compares current metrics with a loaded baseline. It is
// useful for callers that already have a baseline in memory and keeps the
// comparison rules independently testable.
func CompareMetrics(baseline *Baseline, currentMetrics map[string]BenchmarkMetric, threshold float64) (*ComparisonReport, error) {
	if baseline == nil {
		return nil, fmt.Errorf("baseline must not be nil")
	}
	sha, _, err := getCurrentCommit()
	if err != nil {
		return nil, fmt.Errorf("failed to get current commit: %w", err)
	}
	return compareMetrics(baseline, currentMetrics, threshold, sha)
}

func compareMetrics(baseline *Baseline, currentMetrics map[string]BenchmarkMetric, threshold float64, currentSHA string) (*ComparisonReport, error) {
	if threshold == 0 {
		threshold = DefaultThreshold
	}
	if threshold < 0 {
		return nil, fmt.Errorf("regression threshold must not be negative")
	}

	report := &ComparisonReport{
		Timestamp:           time.Now().Format(time.RFC3339),
		CurrentCommitSHA:    currentSHA,
		BaselineCommitSHA:   baseline.CommitSHA,
		BaselineTimestamp:   baseline.Timestamp,
		RegressionThreshold: threshold,
	}

	// Compare each metric
	for benchName, currentMetric := range currentMetrics {
		baselineMetric, exists := baseline.Metrics[benchName]
		if !exists {
			normalizedName := normalizeBenchmarkName(benchName)
			baselineMetric, exists = baseline.Metrics[normalizedName]
		}
		if !exists {
			// Older baselines used the base name and collapsed sub-benchmarks.
			// Keep those files usable while new captures preserve variants.
			baselineMetric, exists = baseline.Metrics[baseBenchmarkName(benchName)]
		}
		if !exists {
			// New benchmark, skip comparison
			continue
		}

		// Compare primary metrics
		report.compareMetric(benchName, "ns_per_op", baselineMetric.NsPerOp, currentMetric.NsPerOp, threshold)
		report.compareMetric(benchName, "mb_per_sec", baselineMetric.MbPerSec, currentMetric.MbPerSec, threshold)
		report.compareMetric(benchName, "bytes_per_op", float64(baselineMetric.BytesPerOp), float64(currentMetric.BytesPerOp), threshold)
		report.compareMetric(benchName, "allocs_per_op", float64(baselineMetric.AllocsPerOp), float64(currentMetric.AllocsPerOp), threshold)

		// Compare custom metrics
		for metricName, currentValue := range currentMetric.CustomMetrics {
			baselineValue, ok := baselineMetric.CustomMetrics[metricName]
			if !ok {
				continue // New custom metric
			}
			if lowerBetter, comparable := metricDirection(metricName); comparable {
				report.compareMetricWithDirection(benchName, metricName, baselineValue, currentValue, threshold, lowerBetter)
			}
		}
	}

	// Determine if test passed (no regressions)
	report.Passed = len(report.Regressions) == 0

	return report, nil
}

// compareMetric compares a single metric value and categorizes the result
func (r *ComparisonReport) compareMetric(benchmarkName, metricName string, baselineValue, currentValue, threshold float64) {
	lowerBetter, comparable := metricDirection(metricName)
	if !comparable {
		return
	}
	r.compareMetricWithDirection(benchmarkName, metricName, baselineValue, currentValue, threshold, lowerBetter)
}

func (r *ComparisonReport) compareMetricWithDirection(benchmarkName, metricName string, baselineValue, currentValue, threshold float64, lowerBetter bool) {
	if baselineValue == 0 {
		// Can't compare with zero baseline
		return
	}

	percentChange := ((currentValue - baselineValue) / baselineValue) * 100

	result := RegressionResult{
		BenchmarkName: benchmarkName,
		MetricName:    metricName,
		BaselineValue: baselineValue,
		CurrentValue:  currentValue,
		PercentChange: percentChange,
		Threshold:     threshold,
	}

	// For performance metrics (lower is better), positive change is a
	// regression. For throughput metrics (higher is better), negative change
	// is a regression.
	if lowerBetter {
		result.IsRegression = percentChange > threshold
	} else {
		// Higher is better (e.g., req/s, throughput)
		result.IsRegression = percentChange < -threshold
	}

	if result.IsRegression {
		r.Regressions = append(r.Regressions, result)
	} else if percentChange >= WarningThreshold || percentChange <= -WarningThreshold {
		// Separate meaningful improvements from changes that move in the
		// wrong direction but remain below the regression threshold.
		improvement := (lowerBetter && percentChange < 0) || (!lowerBetter && percentChange > 0)
		if improvement {
			r.Improvements = append(r.Improvements, result)
		} else {
			r.Warnings = append(r.Warnings, result)
		}
	}
}

// ParseBenchmarkOutput parses go test benchmark output into metrics
func ParseBenchmarkOutput(output string) map[string]BenchmarkMetric {
	metrics := make(map[string]BenchmarkMetric)

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}

		metric := parseBenchmarkLine(line)
		if metric != nil {
			// Keep sub-benchmark variants separate. The GOMAXPROCS suffix is
			// removed because it is an environment detail, not a benchmark
			// identity.
			name := normalizeBenchmarkName(metric.Name)
			metrics[name] = *metric
		}
	}

	return metrics
}

// parseBenchmarkLine parses a single benchmark output line
func parseBenchmarkLine(line string) *BenchmarkMetric {
	// Format: BenchmarkName-8    1000000    1234 ns/op    512 B/op    8 allocs/op
	parts := strings.Fields(line)
	if len(parts) < 3 {
		return nil
	}

	metric := &BenchmarkMetric{
		Name:          parts[0],
		CustomMetrics: make(map[string]float64),
	}

	// Parse iterations
	if iterations, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
		// We don't store iterations directly, but we could
		_ = iterations
	}

	// Parse standard metrics
	for i := 2; i < len(parts); i += 2 {
		if i+1 >= len(parts) {
			break
		}

		valueStr := parts[i]
		unitStr := parts[i+1]

		// Parse value
		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			continue
		}

		// Parse unit and assign to appropriate field
		switch unitStr {
		case "ns/op":
			metric.NsPerOp = value
		case "MB/s":
			metric.MbPerSec = value
		case "B/op":
			metric.BytesPerOp = uint64(value)
		case "allocs/op":
			metric.AllocsPerOp = uint64(value)
		default:
			// Go prints b.ReportMetric values as `<value> <unit>`. Units
			// such as ns/request, req/s, hit_rate%%, and bytes/conn are
			// intentionally retained verbatim for comparison.
			metric.CustomMetrics[unitStr] = value
		}
	}

	return metric
}

// extractBenchmarkName extracts the base benchmark name from a full benchmark name
// e.g., "BenchmarkOpenBaoColdCache/Size-small-8" -> "BenchmarkOpenBaoColdCache"
var benchmarkProcessSuffix = regexp.MustCompile(`-\d+$`)

func normalizeBenchmarkName(fullName string) string {
	return benchmarkProcessSuffix.ReplaceAllString(strings.TrimSpace(fullName), "")
}

func baseBenchmarkName(fullName string) string {
	name := normalizeBenchmarkName(fullName)
	if idx := strings.IndexByte(name, '/'); idx != -1 {
		return name[:idx]
	}
	return name
}

func metricDirection(metricName string) (lowerBetter, comparable bool) {
	name := strings.ToLower(metricName)
	if strings.Contains(name, "ns_per_op") ||
		strings.Contains(name, "bytes") ||
		strings.Contains(name, "allocs") ||
		strings.Contains(name, "latency") ||
		strings.Contains(name, "pause") ||
		strings.Contains(name, "gc_cycles") ||
		strings.Contains(name, "timeouts") ||
		strings.HasPrefix(name, "b/") ||
		strings.HasPrefix(name, "kb/") ||
		strings.HasPrefix(name, "mb/") ||
		strings.HasPrefix(name, "ns/") ||
		strings.HasPrefix(name, "ms/") ||
		strings.HasPrefix(name, "µs_") ||
		strings.HasPrefix(name, "us_") {
		return true, true
	}
	if name == "mb_per_sec" ||
		strings.Contains(name, "req/s") ||
		strings.Contains(name, "req/sec") ||
		strings.Contains(name, "rps") ||
		strings.Contains(name, "throughput") ||
		strings.Contains(name, "success_rate") ||
		strings.Contains(name, "hit_rate") {
		return false, true
	}
	return false, false
}

// FormatReport generates a human-readable report
func FormatReport(report *ComparisonReport) string {
	var buf bytes.Buffer

	buf.WriteString("\n=== Benchmark Regression Report ===\n")
	_, _ = fmt.Fprintf(&buf, "Baseline Commit: %s (%s)\n", shortSHA(report.BaselineCommitSHA), report.BaselineTimestamp)
	_, _ = fmt.Fprintf(&buf, "Current Commit: %s (%s)\n", shortSHA(report.CurrentCommitSHA), report.Timestamp)
	_, _ = fmt.Fprintf(&buf, "Regression Threshold: %.1f%%\n\n", report.RegressionThreshold)

	if len(report.Regressions) > 0 {
		buf.WriteString("❌ REGRESSIONS DETECTED:\n")
		for _, r := range report.Regressions {
			symbol := "⬆"
			if r.PercentChange < 0 {
				symbol = "⬇"
			}
			_, _ = fmt.Fprintf(&buf, "  %s %s.%s: %.2f%% (%.2f → %.2f)\n",
				symbol, r.BenchmarkName, r.MetricName, r.PercentChange,
				r.BaselineValue, r.CurrentValue)
		}
		buf.WriteString("\n")
	}

	if len(report.Warnings) > 0 {
		buf.WriteString("⚠️  WARNINGS (significant change below threshold):\n")
		for _, w := range report.Warnings {
			symbol := "⬆"
			if w.PercentChange < 0 {
				symbol = "⬇"
			}
			_, _ = fmt.Fprintf(&buf, "  %s %s.%s: %.2f%% (%.2f → %.2f)\n",
				symbol, w.BenchmarkName, w.MetricName, w.PercentChange,
				w.BaselineValue, w.CurrentValue)
		}
		buf.WriteString("\n")
	}

	if len(report.Improvements) > 0 {
		buf.WriteString("✅ IMPROVEMENTS:\n")
		for _, i := range report.Improvements {
			symbol := "⬇"
			if i.PercentChange < 0 {
				symbol = "⬆"
			}
			_, _ = fmt.Fprintf(&buf, "  %s %s.%s: %.2f%% (%.2f → %.2f)\n",
				symbol, i.BenchmarkName, i.MetricName, i.PercentChange,
				i.BaselineValue, i.CurrentValue)
		}
		buf.WriteString("\n")
	}

	if report.Passed {
		buf.WriteString("✅ PASSED: No regressions detected\n")
	} else {
		_, _ = fmt.Fprintf(&buf, "❌ FAILED: %d regression(s) detected\n", len(report.Regressions))
	}

	return buf.String()
}

func shortSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}
