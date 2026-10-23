package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v4"
)

// The x-upstream-map / x-fanout-scope decoders live here because their value
// shapes are specified in docs/plan/plan.md (Data Models, "specified here and
// nowhere else"), and the shapes a yaml.Node can arrive as are exactly where
// the Phase 10 runtime failure came from: the plan makes requiredScope an
// array with bare-string sugar and injectAs an object, while a decode target
// of []string / json.RawMessage accepts neither documented form. These
// helpers normalize the documented forms before any Go-typed handoff.

// scopeListNode decodes a scope requirement that may arrive as a bare string
// (one-element sugar, the same sugar x-required-scope documents) or as an
// array of scope strings. A null or absent node yields no scopes.
func scopeListNode(node *yaml.Node) ([]string, error) {
	if node == nil || node.Kind == 0 || node.Tag == "!!null" {
		return nil, nil
	}
	switch node.Kind {
	case yaml.SequenceNode:
		var scopes []string
		if err := node.Decode(&scopes); err != nil {
			return nil, fmt.Errorf("scope list entries must be strings: %w", err)
		}
		return scopes, nil
	case yaml.ScalarNode:
		var scope string
		if err := node.Decode(&scope); err != nil {
			return nil, fmt.Errorf("must be a scope string or array of scope strings: %w", err)
		}
		if strings.TrimSpace(scope) == "" {
			return nil, nil
		}
		return []string{scope}, nil
	default:
		return nil, fmt.Errorf("must be a scope string or array of scope strings")
	}
}

// injectAsRaw converts an injectAs value into the JSON bytes
// decodeInjectAsRaw parses. The plan specifies injectAs as an object
// ({kind, name}), which arrives as a YAML mapping; the JSON-encoded string
// form is likewise accepted, since that has been the storage shape for
// json.RawMessage handoffs since Phase 10. A null or absent node means no
// injection.
func injectAsRaw(node *yaml.Node) (json.RawMessage, error) {
	if node == nil || node.Kind == 0 || node.Tag == "!!null" {
		return nil, nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		var encoded string
		if err := node.Decode(&encoded); err != nil {
			return nil, fmt.Errorf("injectAs must be an object or list: %w", err)
		}
		trimmed := strings.TrimSpace(encoded)
		if trimmed == "" {
			return nil, nil
		}
		return json.RawMessage(trimmed), nil
	case yaml.MappingNode, yaml.SequenceNode:
		var value interface{}
		if err := node.Decode(&value); err != nil {
			return nil, fmt.Errorf("injectAs must be an object or list: %w", err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("injectAs could not be encoded: %w", err)
		}
		return json.RawMessage(encoded), nil
	default:
		return nil, fmt.Errorf("injectAs must be an object or list")
	}
}
