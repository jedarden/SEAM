package baseline

import (
	"strings"
	"testing"
)

func TestParseBenchmarkOutputPreservesVariantsAndCustomMetrics(t *testing.T) {
	output := strings.Join([]string{
		"BenchmarkProxy/Small-8 100 1200 ns/op 64 B/op 2 allocs/op 900 ns/request 1200 req/s",
		"BenchmarkProxy/Large-8 50 2400 ns/op 128 B/op 4 allocs/op 1800 ns/request 600 req/s",
		"BenchmarkRate-8 10 2.5 MB/s 32 B/op 1 allocs/op",
	}, "\n")

	metrics := ParseBenchmarkOutput(output)
	if len(metrics) != 3 {
		t.Fatalf("expected three benchmark variants, got %d: %#v", len(metrics), metrics)
	}

	small, ok := metrics["BenchmarkProxy/Small"]
	if !ok {
		t.Fatalf("missing normalized small variant: %#v", metrics)
	}
	if got := small.CustomMetrics["ns/request"]; got != 900 {
		t.Fatalf("ns/request = %v, want 900", got)
	}
	if got := small.CustomMetrics["req/s"]; got != 1200 {
		t.Fatalf("req/s = %v, want 1200", got)
	}

	rate := metrics["BenchmarkRate"]
	if rate.MbPerSec != 2.5 {
		t.Fatalf("MB/s = %v, want 2.5", rate.MbPerSec)
	}
}

func TestCompareMetricsDetectsLatencyAndThroughputRegressions(t *testing.T) {
	baseline := &Baseline{
		CommitSHA: "base",
		Metrics: map[string]BenchmarkMetric{
			"BenchmarkProxy/Small": {
				NsPerOp:     100,
				MbPerSec:    100,
				BytesPerOp:  100,
				AllocsPerOp: 2,
				CustomMetrics: map[string]float64{
					"ns/request": 100,
					"req/s":      1000,
				},
			},
		},
	}

	current := map[string]BenchmarkMetric{
		"BenchmarkProxy/Small": {
			NsPerOp:     120,
			MbPerSec:    80,
			BytesPerOp:  120,
			AllocsPerOp: 2,
			CustomMetrics: map[string]float64{
				"ns/request": 120,
				"req/s":      800,
			},
		},
	}

	report, err := compareMetrics(baseline, current, 10, "current")
	if err != nil {
		t.Fatalf("compareMetrics() error = %v", err)
	}
	if report.Passed {
		t.Fatal("expected regression report to fail")
	}
	if len(report.Regressions) != 5 {
		t.Fatalf("got %d regressions, want 5: %#v", len(report.Regressions), report.Regressions)
	}
}

func TestCompareMetricsClassifiesThroughputImprovement(t *testing.T) {
	baseline := &Baseline{Metrics: map[string]BenchmarkMetric{
		"BenchmarkProxy": {CustomMetrics: map[string]float64{"req/s": 1000}},
	}}
	current := map[string]BenchmarkMetric{
		"BenchmarkProxy-20": {CustomMetrics: map[string]float64{"req/s": 1100}},
	}

	report, err := compareMetrics(baseline, current, 10, "current")
	if err != nil {
		t.Fatalf("compareMetrics() error = %v", err)
	}
	if !report.Passed || len(report.Improvements) != 1 {
		t.Fatalf("expected one throughput improvement, got passed=%t improvements=%d regressions=%d", report.Passed, len(report.Improvements), len(report.Regressions))
	}
}

func TestFormatReportHandlesShortCommitSHAs(t *testing.T) {
	report := &ComparisonReport{
		BaselineCommitSHA:   "base",
		CurrentCommitSHA:    "current",
		RegressionThreshold: 10,
		Passed:              true,
	}
	formatted := FormatReport(report)
	if !strings.Contains(formatted, "Baseline Commit: base") || !strings.Contains(formatted, "Current Commit: current") {
		t.Fatalf("short commit SHA formatting lost values: %s", formatted)
	}
}

func TestValidateBenchmarkType(t *testing.T) {
	if err := ValidateBenchmarkType("openbao"); err != nil {
		t.Fatalf("known benchmark type rejected: %v", err)
	}
	if err := ValidateBenchmarkType("unknown"); err == nil {
		t.Fatal("unknown benchmark type accepted")
	}
}
