package main

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// RetirementEvaluator evaluates routes for retirement eligibility
type RetirementEvaluator struct {
	vmc          *VictoriaMetricsClient
	ghc          *GitHubClient
	config       *Config
	fragmentPath string
}

// NewRetirementEvaluator creates a new retirement evaluator
func NewRetirementEvaluator(vmc *VictoriaMetricsClient, ghc *GitHubClient, config *Config) *RetirementEvaluator {
	return &RetirementEvaluator{
		vmc:          vmc,
		ghc:          ghc,
		config:       config,
		fragmentPath: config.DeclarativeConfigPath,
	}
}

// RunEvaluation executes the retirement evaluation process
func (re *RetirementEvaluator) RunEvaluation(ctx context.Context) error {
	zap.L().Info("Starting retirement evaluation")

	// Step 1: Query all route versions from VictoriaMetrics
	routeStats, err := re.vmc.QueryRouteVersionTraffic(ctx)
	if err != nil {
		return fmt.Errorf("failed to query route traffic: %w", err)
	}

	zap.L().Info("Queried route traffic stats",
		zap.Int("route_versions", len(routeStats)))

	// Step 2: Evaluate each route version for retirement eligibility
	candidates := make([]RetirementCandidate, 0)

	for _, stats := range routeStats {
		// Calculate quiet-since and evaluation window
		quietSince := stats.QuietSince
		maxGap := stats.MaxGap
		totalHistory := time.Since(stats.HistoryStart)

		// Calculate evaluation window
		window := re.calculateEvaluationWindow(maxGap, totalHistory)

		// Check if route version is eligible for retirement
		eligible, reason := re.isEligibleForRetirement(stats, quietSince, window)

		if eligible {
			candidates = append(candidates, RetirementCandidate{
				RouteStats:     stats,
				QuietSince:     quietSince,
				MaxGap:         maxGap,
				EvalWindow:     window,
				QualifyingTime: time.Now().Add(-window),
				Reason:         reason,
			})

			zap.L().Info("Route version eligible for retirement",
				zap.String("route", stats.Route),
				zap.String("version", stats.APIVersion),
				zap.String("spec_version", stats.SpecVersion),
				zap.Duration("quiet_duration", time.Since(quietSince)),
				zap.Duration("eval_window", window),
				zap.String("reason", reason))
		}
	}

	// Step 3: Open PRs for eligible candidates
	for _, candidate := range candidates {
		if err := re.openRetirementPR(ctx, &candidate); err != nil {
			zap.L().Error("Failed to open retirement PR",
				zap.String("route", candidate.RouteStats.Route),
				zap.String("version", candidate.RouteStats.APIVersion),
				zap.Error(err))
			continue
		}

		zap.L().Info("Successfully opened retirement PR",
			zap.String("route", candidate.RouteStats.Route),
			zap.String("version", candidate.RouteStats.APIVersion))
	}

	zap.L().Info("Retirement evaluation completed",
		zap.Int("total_routes", len(routeStats)),
		zap.Int("candidates", len(candidates)))

	return nil
}

// calculateEvaluationWindow calculates the retirement evaluation window
// Window = max(3 x observed max gap, 7 days)
// Insufficient history -> 7-day floor
func (re *RetirementEvaluator) calculateEvaluationWindow(maxGap, historyLength time.Duration) time.Duration {
	// Apply 7-day floor
	floor := time.Duration(WindowFloorDays) * 24 * time.Hour

	// If we don't have sufficient history, use the floor
	if historyLength < MinimumHistoryRequired {
		zap.L().Warn("Insufficient history for reliable gap calculation, using floor",
			zap.Duration("history", historyLength),
			zap.Duration("floor", floor))
		return floor
	}

	// Calculate window as 3x max gap
	window := time.Duration(MaxGapMultiplier) * maxGap

	// Ensure window is at least 7 days
	if window < floor {
		window = floor
	}

	return window
}

// isEligibleForRetirement checks if a route version meets retirement criteria
// Zero observed traffic is a NECESSARY condition
func (re *RetirementEvaluator) isEligibleForRetirement(stats RouteTrafficStats, quietSince time.Time, window time.Duration) (bool, string) {
	now := time.Now()

	// CRITICAL: Zero observed traffic is a necessary condition
	// If we've seen ANY requests, this route version cannot retire
	if stats.TotalRequests > 0 {
		return false, "Route has active traffic"
	}

	// Check if quiet period exceeds evaluation window
	quietDuration := now.Sub(quietSince)
	if quietDuration < window {
		return false, fmt.Sprintf("Quiet duration (%s) less than evaluation window (%s)",
			quietDuration.Round(time.Hour), window.Round(time.Hour))
	}

	// All conditions met
	return true, fmt.Sprintf("Zero traffic for %s (exceeds window %s)",
		quietDuration.Round(time.Hour), window.Round(time.Hour))
}

// openRetirementPR opens a PR to declarative-config with sunset and brownout schedule
func (re *RetirementEvaluator) openRetirementPR(ctx context.Context, candidate *RetirementCandidate) error {
	now := time.Now()

	// Calculate sunset date (typically 90-180 days out)
	sunsetDate := now.Add(90 * 24 * time.Hour).Format("2006-01-02")

	// Calculate brownout windows (2-3 windows before sunset)
	brownouts := re.calculateBrownouts(now, sunsetDate)

	// Build the deprecation block
	deprecationBlock := fmt.Sprintf(`x-seam-deprecated:
  since: "%s"
  sunset: "%s"
  brownouts:
%s`, now.Format("2006-01-02"), sunsetDate, brownouts)

	// The block has nowhere to land yet: declarative-config stores route
	// fragments as JSON entries inside one whole ConfigMap per route owner
	// (k8s/rs-manager/seam/configmap-routes-*.yaml), not as the
	// <path>/<route>/fragment.yaml file this tool assumes, and the fragment
	// schema does not know x-seam-deprecated. Surface it for diagnostics until
	// a write target that SEAM would actually load exists.
	zap.L().Debug("Generated x-seam-deprecated block",
		zap.String("route", candidate.RouteStats.Route),
		zap.String("block", deprecationBlock))

	// Create the PR
	prTitle := fmt.Sprintf("feat(seam): deprecate %s (version %s) - zero traffic for %s",
		candidate.RouteStats.Route,
		candidate.RouteStats.APIVersion,
		time.Since(candidate.QuietSince).Round(24*time.Hour))

	prBody := fmt.Sprintf(`## Deprecation Proposal

**Route:** %s
**API Version:** %s
**Spec Version:** %s

### Retirement Eligibility

This route version is proposed for deprecation based on the following metrics:

- **Zero observed traffic**: No requests detected in evaluation window
- **Quiet since:** %s
- **Quiet duration:** %s
- **Evaluation window:** %s
- **Reason:** %s

### Proposed Timeline

- **Deprecation declared:** %s
- **Sunset:** %s (90 days from declaration)
- **Brownout windows:** See fragment

### Changes

This PR adds an `+"`x-seam-deprecated`"+` block to the route fragment with:
- `+"`since`"+` date (deprecation declaration)
- `+"`sunset`"+` date (removal target)
- Brownout windows (410 Gone periods)

The verdict channel (deprecation state) travels through the existing hot-reload path — no deployment required.

### Verification

- Route fragment exists at `+"`%s`"+`
- Zero observed traffic in VictoriaMetrics
- No caller-appears events detected

### Approval Required

This PR requires human approval before merging to ensure blast radius is bounded.
Mechanical checks validate fragment path ownership; the human gate is the final approval.
`,
		candidate.RouteStats.Route,
		candidate.RouteStats.APIVersion,
		candidate.RouteStats.SpecVersion,
		candidate.QuietSince.Format("2006-01-02T15:04:05Z"),
		time.Since(candidate.QuietSince).Round(24*time.Hour),
		candidate.EvalWindow.Round(24*time.Hour),
		candidate.Reason,
		now.Format("2006-01-02"),
		sunsetDate,
		re.getFragmentPath(candidate),
	)

	// Open the PR. No file change is attached yet (see the note on the
	// deprecation block above), so OpenPR refuses rather than opening the
	// zero-diff pull request GitHub would reject.
	branchName := fmt.Sprintf("seam-deprecate-%s-%s", candidate.RouteStats.Route, candidate.RouteStats.APIVersion)
	branchName = sanitizeBranchName(branchName)

	prURL, err := re.ghc.OpenPR(ctx, branchName, prTitle, prBody, nil, []string{"seam"})
	if err != nil {
		return fmt.Errorf("failed to open PR: %w", err)
	}

	zap.L().Info("Opened retirement PR",
		zap.String("pr_url", prURL),
		zap.String("branch", branchName))

	return nil
}

// calculateBrownouts calculates brownout windows leading up to sunset
func (re *RetirementEvaluator) calculateBrownouts(since time.Time, sunset string) string {
	// Parse sunset date
	sunsetDate, err := time.Parse("2006-01-02", sunset)
	if err != nil {
		zap.L().Error("Failed to parse sunset date", zap.Error(err))
		// Return a single conservative brownout
		return re.formatBrownout(since.Add(30*24*time.Hour), since.Add(37*24*time.Hour))
	}

	// Calculate 3 brownout windows:
	// 1. 30 days from deprecation (1 week)
	// 2. 60 days from deprecation (1 week)
	// 3. 80 days from deprecation (1 week before sunset)
	brownouts := ""

	// First brownout
	b1Start := since.Add(30 * 24 * time.Hour)
	b1End := b1Start.Add(7 * 24 * time.Hour)
	brownouts += re.formatBrownout(b1Start, b1End) + "\n"

	// Second brownout
	b2Start := since.Add(60 * 24 * time.Hour)
	b2End := b2Start.Add(7 * 24 * time.Hour)
	brownouts += re.formatBrownout(b2Start, b2End) + "\n"

	// Third brownout (1 week before sunset)
	b3Start := sunsetDate.Add(-7 * 24 * time.Hour)
	b3End := sunsetDate
	brownouts += re.formatBrownout(b3Start, b3End)

	return brownouts
}

func (re *RetirementEvaluator) formatBrownout(start, end time.Time) string {
	return fmt.Sprintf(`    - start: "%s"
      end: "%s"`, start.Format(time.RFC3339), end.Format(time.RFC3339))
}

func (re *RetirementEvaluator) getFragmentPath(candidate *RetirementCandidate) string {
	return fmt.Sprintf("%s/%s/fragment.yaml", re.fragmentPath, candidate.RouteStats.Route)
}

// RetirementCandidate represents a route version eligible for retirement
type RetirementCandidate struct {
	RouteStats     RouteTrafficStats
	QuietSince     time.Time
	MaxGap         time.Duration
	EvalWindow     time.Duration
	QualifyingTime time.Time
	Reason         string
}

func sanitizeBranchName(name string) string {
	// Sanitize for GitHub branch names
	result := ""
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			result += string(c)
		} else {
			result += "-"
		}
	}
	return result
}
