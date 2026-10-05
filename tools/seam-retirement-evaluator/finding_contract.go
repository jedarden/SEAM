package main

import "go.uber.org/zap"

const (
	// retirementFindingMessage is the stable message selector used by the
	// handoff runbook. The fields below are the complete candidate payload.
	retirementFindingMessage = "Deprecation candidate detected"
	candidateMetricName      = "seam_retirement_deprecation_candidates_total"
)

// retirementFindingField describes one payload field in the stable finding
// schema. The schema is deliberately all required strings: a partial finding
// is not actionable, and there are no optional fields or extension fields.
type retirementFindingField struct {
	name     string
	typeName string
	required bool
}

// retirementFindingSchema is intentionally a closed list. A finding may
// contain route-version identity and the proposal derived from it, but it
// must not grow an HTTP header, query response, credential, or arbitrary
// source-metric label field. Keep this list in the same order as fields so
// serialized production logs remain stable for operators and consumers.
var retirementFindingSchema = [...]retirementFindingField{
	{name: "route", typeName: "string", required: true},
	{name: "api_version", typeName: "string", required: true},
	{name: "spec_version", typeName: "string", required: true},
	{name: "quiet_since", typeName: "string", required: true},
	{name: "eval_window", typeName: "string", required: true},
	{name: "reason", typeName: "string", required: true},
	{name: "proposed_sunset", typeName: "string", required: true},
	{name: "brownout_windows", typeName: "string", required: true},
	{name: "fragment_path", typeName: "string", required: true},
	{name: "x_seam_deprecated_block", typeName: "string", required: true},
	{name: "body", typeName: "string", required: true},
}

var retirementFindingFields = findingFieldNames()

func findingFieldNames() [len(retirementFindingSchema)]string {
	var names [len(retirementFindingSchema)]string
	for i, field := range retirementFindingSchema {
		names[i] = field.name
	}
	return names
}

// candidateMetricLabels is the complete label set for the per-candidate
// counter. It contains identity only; timestamps, caller identity, status,
// and source-metric labels are deliberately not labels.
var candidateMetricLabels = [...]string{"route", "api_version", "spec_version"}

// retirementFinding is the structured payload emitted for one candidate.
// Keeping this as a concrete type makes it difficult for the log and metric
// paths to silently drift apart: both are built from the same route-version
// identity.
type retirementFinding struct {
	route            string
	apiVersion       string
	specVersion      string
	quietSince       string
	evaluationWindow string
	reason           string
	proposedSunset   string
	brownoutWindows  string
	fragmentPath     string
	deprecatedBlock  string
	body             string
}

func (f retirementFinding) fields() []zap.Field {
	return []zap.Field{
		zap.String("route", f.route),
		zap.String("api_version", f.apiVersion),
		zap.String("spec_version", f.specVersion),
		zap.String("quiet_since", f.quietSince),
		zap.String("eval_window", f.evaluationWindow),
		zap.String("reason", f.reason),
		zap.String("proposed_sunset", f.proposedSunset),
		zap.String("brownout_windows", f.brownoutWindows),
		zap.String("fragment_path", f.fragmentPath),
		zap.String("x_seam_deprecated_block", f.deprecatedBlock),
		zap.String("body", f.body),
	}
}

func (f retirementFinding) metricKey() routeVersionKey {
	return routeVersionKey{
		route:       f.route,
		apiVersion:  f.apiVersion,
		specVersion: f.specVersion,
	}
}
