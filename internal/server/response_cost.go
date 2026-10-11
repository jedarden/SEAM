package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	maxResponseCostCaptureBytes = 1 << 20
	maxSSEScanLineBytes         = maxResponseCostCaptureBytes
)

type responseCostBody struct {
	io.ReadCloser
	tail bytes.Buffer
}

func newResponseCostBody(body io.ReadCloser) *responseCostBody {
	return &responseCostBody{ReadCloser: body}
}

func (b *responseCostBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		if n >= maxResponseCostCaptureBytes {
			b.tail.Reset()
			_, _ = b.tail.Write(p[n-maxResponseCostCaptureBytes : n])
		} else {
			if b.tail.Len()+n > maxResponseCostCaptureBytes {
				discard := b.tail.Len() + n - maxResponseCostCaptureBytes
				copyBuf := append([]byte(nil), b.tail.Bytes()[discard:]...)
				b.tail.Reset()
				_, _ = b.tail.Write(copyBuf)
			}
			_, _ = b.tail.Write(p[:n])
		}
	}
	return n, err
}

func (b *responseCostBody) captured() []byte {
	return append([]byte(nil), b.tail.Bytes()...)
}

// ResponseCostCharge describes one usage counter in a provider response.
// Pointer is an RFC 6901 JSON Pointer and Price is the price for one unit of
// the pointed-at counter in the route's x-cost-per-call unit.
type ResponseCostCharge struct {
	Pointer string
	Price   float64
}

// ResponseCostConfig is the provider-agnostic response metering contract.
// Every pointer must resolve to a finite JSON number for a response-derived
// charge to be used; otherwise the caller supplies the flat fallback.
type ResponseCostConfig struct {
	Charges []ResponseCostCharge
}

// CostPerCallConfig is the flat fallback and retains the unit needed by the
// post-response budget header.
type CostPerCallConfig struct {
	Amount float64
	Unit   string
}

func parseCostPerCallConfig(raw any) (*CostPerCallConfig, error) {
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("x-cost-per-call must be an object")
	}
	amount, ok := jsonNumber(value["amount"])
	if !ok || amount < 0 {
		return nil, fmt.Errorf("x-cost-per-call.amount must be a non-negative number")
	}
	unit, ok := value["unit"].(string)
	if !ok || strings.TrimSpace(unit) == "" {
		return nil, fmt.Errorf("x-cost-per-call.unit must be a non-empty string")
	}
	return &CostPerCallConfig{Amount: amount, Unit: unit}, nil
}

func (c *ResponseCostConfig) validate() error {
	if c == nil || len(c.Charges) == 0 {
		return fmt.Errorf("x-cost-from-response requires at least one pointer")
	}
	for _, charge := range c.Charges {
		if charge.Pointer != "" && !strings.HasPrefix(charge.Pointer, "/") {
			return fmt.Errorf("x-cost-from-response pointer %q must be empty or start with '/'", charge.Pointer)
		}
		if charge.Price < 0 {
			return fmt.Errorf("x-cost-from-response price for %q must be non-negative", charge.Pointer)
		}
	}
	return nil
}

// responseCost evaluates the final JSON body, or the last JSON SSE event.
// The boolean reports whether all configured pointers resolved successfully.
func (c *ResponseCostConfig) responseCost(body []byte, streaming bool) (float64, bool) {
	if c == nil || len(c.Charges) == 0 {
		return 0, false
	}
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
		return 0, false
	}

	var total float64
	for _, charge := range c.Charges {
		value, ok := jsonPointerValue(document, charge.Pointer)
		if !ok {
			return 0, false
		}
		number, ok := jsonNumber(value)
		if !ok {
			return 0, false
		}
		total += number * charge.Price
	}
	return total, true
}

func jsonNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case uint:
		return float64(number), true
	case uint64:
		return float64(number), true
	default:
		return 0, false
	}
}

// jsonPointerValue implements the RFC 6901 object and array traversal rules.
func jsonPointerValue(document any, pointer string) (any, bool) {
	if pointer == "" {
		return document, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := document
	for _, rawToken := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(rawToken, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[token]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(value) {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// lastSSEJSON returns the data payload from the last complete SSE event.
// A provider's terminal [DONE] event is deliberately ignored.
func lastSSEJSON(body []byte) []byte {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 1024), maxSSEScanLineBytes)
	var event bytes.Buffer
	var last []byte
	flush := func() {
		candidate := strings.TrimSpace(event.String())
		event.Reset()
		if candidate == "" || candidate == "[DONE]" || !json.Valid([]byte(candidate)) {
			return
		}
		last = append(last[:0], candidate...)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if event.Len() > 0 {
				event.WriteByte('\n')
			}
			event.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	return last
}

// parseResponseCostConfig accepts the documented array form and the keyed
// object form used by provider fragments. Supporting both keeps fragments
// readable for providers that call their counters input/output or prompt/
// completion while the runtime retains one normalized representation.
func parseResponseCostConfig(raw any) (*ResponseCostConfig, error) {
	config := &ResponseCostConfig{}
	appendCharge := func(value any, key string) error {
		item, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("x-cost-from-response entry %q must be an object", key)
		}
		pointer, _ := item["pointer"].(string)
		if pointer == "" {
			if key == "" || strings.HasPrefix(key, "pointer") {
				return fmt.Errorf("x-cost-from-response entry requires pointer")
			}
			pointer = key
		}
		price, ok := responseCostPrice(item)
		if !ok {
			return fmt.Errorf("x-cost-from-response entry %q requires price", key)
		}
		config.Charges = append(config.Charges, ResponseCostCharge{Pointer: pointer, Price: price})
		return nil
	}

	switch value := raw.(type) {
	case []any:
		for index, item := range value {
			if err := appendCharge(item, strconv.Itoa(index)); err != nil {
				return nil, err
			}
		}
	case map[string]any:
		if charges, ok := value["charges"]; ok {
			return parseResponseCostConfig(charges)
		}
		if pointers, ok := value["pointers"]; ok {
			return parseResponseCostConfig(pointers)
		}
		if _, hasPointer := value["pointer"]; hasPointer {
			if err := appendCharge(value, ""); err != nil {
				return nil, err
			}
		} else {
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			for _, key := range keys {
				if err := appendCharge(value[key], key); err != nil {
					return nil, err
				}
			}
		}
	default:
		return nil, fmt.Errorf("x-cost-from-response must be an object or list")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return config, nil
}

func responseCostPrice(item map[string]any) (float64, bool) {
	for _, name := range []string{"price", "unitPrice", "perUnitPrice"} {
		if value, ok := item[name]; ok {
			number, ok := jsonNumber(value)
			return number, ok
		}
	}
	return 0, false
}
