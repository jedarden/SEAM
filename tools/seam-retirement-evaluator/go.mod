module github.com/ardenone/seam/tools/seam-retirement-evaluator

go 1.25.0

require (
	go.uber.org/zap v1.27.0
	// Test-only: the output-contract tests parse the proposed block as YAML
	// to prove it is fragment-shaped before a human lands it.
	gopkg.in/yaml.v3 v3.0.1
)

require go.uber.org/multierr v1.10.0 // indirect
