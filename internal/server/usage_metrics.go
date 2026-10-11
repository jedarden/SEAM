package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const otherModelLabel = "other"

const maxMetricRequestCaptureBytes = 1 << 20

type metricRequestBodyContextKey struct{}

type metricRequestBodyCapture struct {
	io.ReadCloser
	data     bytes.Buffer
	overflow bool
}

func newMetricRequestBodyCapture(body io.ReadCloser) *metricRequestBodyCapture {
	if body == nil || body == http.NoBody {
		return nil
	}
	return &metricRequestBodyCapture{ReadCloser: body}
}

func (c *metricRequestBodyCapture) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if n > 0 {
		remaining := maxMetricRequestCaptureBytes - c.data.Len()
		if remaining > 0 {
			_, _ = c.data.Write(p[:minInt(n, remaining)])
		}
		if n > remaining {
			c.overflow = true
		}
	}
	return n, err
}

func (c *metricRequestBodyCapture) captured() []byte {
	if c == nil || c.overflow {
		return nil
	}
	return append([]byte(nil), c.data.Bytes()...)
}

func metricRequestBodyCaptureFromRequest(r *http.Request) *metricRequestBodyCapture {
	if r == nil {
		return nil
	}
	capture, _ := r.Context().Value(metricRequestBodyContextKey{}).(*metricRequestBodyCapture)
	return capture
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// UsageMetricsConfig describes the response usage fields and request model
// field used by the route metrics. Pointers are RFC 6901 JSON Pointers. The
// model allowlist is deliberately stored as a slice so its declared order is
// retained in the route table; metric lookup uses the bounded set below.
type UsageMetricsConfig struct {
	InputTokensPointer  string
	OutputTokensPointer string
	Model               ModelMetricConfig
}

type ModelMetricConfig struct {
	Pointer   string
	Allowlist []string
}

func (c *UsageMetricsConfig) validate() error {
	if c == nil {
		return nil
	}
	for name, pointer := range map[string]string{
		"inputTokens":  c.InputTokensPointer,
		"outputTokens": c.OutputTokensPointer,
		"model":        c.Model.Pointer,
	} {
		if pointer != "" && !strings.HasPrefix(pointer, "/") {
			return fmt.Errorf("x-metrics.%s pointer %q must be empty or start with '/'", name, pointer)
		}
	}
	if c.Model.Pointer == "" && len(c.Model.Allowlist) > 0 {
		return fmt.Errorf("x-metrics.model.allowlist requires x-metrics.model.pointer")
	}
	seen := make(map[string]struct{}, len(c.Model.Allowlist))
	for _, model := range c.Model.Allowlist {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("x-metrics.model.allowlist entries must be non-empty strings")
		}
		if _, exists := seen[model]; exists {
			return fmt.Errorf("x-metrics.model.allowlist contains duplicate %q", model)
		}
		seen[model] = struct{}{}
	}
	if c.Model.Pointer != "" && len(c.Model.Allowlist) == 0 {
		return fmt.Errorf("x-metrics.model requires a non-empty allowlist")
	}
	return nil
}

// parseUsageMetricsConfig accepts the canonical nested form:
//
//	x-metrics:
//	  usage:
//	    inputTokens: /usage/input_tokens
//	    outputTokens: /usage/output_tokens
//	  model:
//	    pointer: /model
//	    allowlist: [provider-model-a, provider-model-b]
//
// Direct inputTokens/outputTokens keys and snake_case aliases are accepted as
// equivalent spellings so fragments can follow provider vocabulary without
// changing the runtime representation.
func parseUsageMetricsConfig(raw any) (*UsageMetricsConfig, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("x-metrics must be an object")
	}
	config := &UsageMetricsConfig{}
	usage := object
	if nested, exists := object["usage"]; exists {
		var ok bool
		usage, ok = nested.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("x-metrics.usage must be an object")
		}
	}
	var err error
	config.InputTokensPointer, err = metricPointer(usage, "inputTokens", "input_tokens", "input", "promptTokens", "prompt_tokens", "prompt")
	if err != nil {
		return nil, fmt.Errorf("x-metrics input token pointer: %w", err)
	}
	config.OutputTokensPointer, err = metricPointer(usage, "outputTokens", "output_tokens", "output", "completionTokens", "completion_tokens", "completion")
	if err != nil {
		return nil, fmt.Errorf("x-metrics output token pointer: %w", err)
	}

	modelValue, exists := object["model"]
	if !exists {
		modelValue = object["modelLabel"]
	}
	if modelValue != nil {
		modelObject, ok := modelValue.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("x-metrics.model must be an object")
		}
		config.Model.Pointer, err = stringPointer(modelObject["pointer"])
		if err != nil {
			return nil, fmt.Errorf("x-metrics.model.pointer: %w", err)
		}
		allowlist, exists := modelObject["allowlist"]
		if !exists {
			allowlist = modelObject["allowed"]
		}
		if allowlist != nil {
			config.Model.Allowlist, err = stringList(allowlist)
			if err != nil {
				return nil, fmt.Errorf("x-metrics.model.allowlist: %w", err)
			}
		}
	}
	if config.Model.Pointer == "" {
		if pointer, exists := object["modelPointer"]; exists {
			config.Model.Pointer, err = stringPointer(pointer)
			if err != nil {
				return nil, fmt.Errorf("x-metrics.modelPointer: %w", err)
			}
		}
	}
	if len(config.Model.Allowlist) == 0 {
		if allowlist, exists := object["modelAllowlist"]; exists {
			config.Model.Allowlist, err = stringList(allowlist)
			if err != nil {
				return nil, fmt.Errorf("x-metrics.modelAllowlist: %w", err)
			}
		}
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return config, nil
}

func metricPointer(object map[string]any, names ...string) (string, error) {
	for _, name := range names {
		value, exists := object[name]
		if !exists {
			continue
		}
		return stringPointer(value)
	}
	return "", nil
}

func stringPointer(value any) (string, error) {
	switch pointer := value.(type) {
	case string:
		return strings.TrimSpace(pointer), nil
	case map[string]any:
		pointerValue, ok := pointer["pointer"].(string)
		if !ok {
			return "", fmt.Errorf("must be a JSON Pointer string or an object with pointer")
		}
		return strings.TrimSpace(pointerValue), nil
	default:
		return "", fmt.Errorf("must be a JSON Pointer string")
	}
}

func stringList(value any) ([]string, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("must be an array")
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		model, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("entries must be strings")
		}
		result = append(result, model)
	}
	return result, nil
}

func (c *UsageMetricsConfig) modelLabel(body []byte) string {
	if c == nil || c.Model.Pointer == "" || len(body) == 0 {
		return otherModelLabel
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return otherModelLabel
	}
	value, found := jsonPointerValue(document, c.Model.Pointer)
	model, ok := value.(string)
	if !found || !ok || !containsStringValue(c.Model.Allowlist, model) {
		return otherModelLabel
	}
	return model
}

func containsStringValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func usageMetricsFromResponseCost(config *ResponseCostConfig) *UsageMetricsConfig {
	if config == nil {
		return nil
	}
	metrics := &UsageMetricsConfig{}
	for _, charge := range config.Charges {
		kind := strings.ToLower(charge.Name)
		if kind == "" {
			kind = strings.ToLower(lastJSONPointerToken(charge.Pointer))
		}
		switch {
		case strings.Contains(kind, "input"), strings.Contains(kind, "prompt"):
			if metrics.InputTokensPointer == "" {
				metrics.InputTokensPointer = charge.Pointer
			}
		case strings.Contains(kind, "output"), strings.Contains(kind, "completion"):
			if metrics.OutputTokensPointer == "" {
				metrics.OutputTokensPointer = charge.Pointer
			}
		}
	}
	if metrics.InputTokensPointer == "" && metrics.OutputTokensPointer == "" {
		return nil
	}
	return metrics
}

func lastJSONPointerToken(pointer string) string {
	if pointer == "" {
		return ""
	}
	parts := strings.Split(pointer, "/")
	return strings.ReplaceAll(strings.ReplaceAll(parts[len(parts)-1], "~1", "/"), "~0", "~")
}

func responseUsageDocument(body []byte, streaming bool) (any, bool) {
	payload := body
	if streaming {
		payload = lastSSEJSON(body)
		if len(payload) == 0 {
			payload = body
		}
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, false
	}
	return document, true
}
