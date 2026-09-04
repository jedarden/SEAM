package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// jsonPointer implements RFC 6901 JSON Pointer operations
type jsonPointer struct {
	tokens []string
}

// newJSONPointer creates a new JSON Pointer from a string
func newJSONPointer(ptr string) (*jsonPointer, error) {
	if ptr == "" {
		return &jsonPointer{tokens: []string{}}, nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, fmt.Errorf("json pointer must start with /")
	}

	tokens := strings.Split(ptr[1:], "/")
	// Unescape tokens
	for i, token := range tokens {
		token = strings.ReplaceAll(token, "~1", "/")
		token = strings.ReplaceAll(token, "~0", "~")
		tokens[i] = token
	}

	return &jsonPointer{tokens: tokens}, nil
}

// Get resolves the JSON Pointer and returns the referenced value
func (p *jsonPointer) Get(data any) (any, bool, error) {
	current := data

	for _, token := range p.tokens {
		switch v := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = v[token]
			if !ok {
				return nil, false, nil
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil {
				return nil, false, fmt.Errorf("invalid array index: %s", token)
			}
			if index < 0 || index >= len(v) {
				return nil, false, nil
			}
			current = v[index]
		default:
			return nil, false, fmt.Errorf("cannot index into non-container type")
		}
	}

	return current, true, nil
}

// jsonPointerGet is a convenience function for Get
func jsonPointerGet(data any, ptr string) (any, bool, error) {
	p, err := newJSONPointer(ptr)
	if err != nil {
		return nil, false, err
	}
	return p.Get(data)
}

// AdapterConfig holds the x-adapter configuration for a route
type AdapterConfig struct {
	TargetVersion      string
	RequestTransforms  []AdapterTransform
	ResponseTransforms []AdapterTransform
}

// AdapterTransform represents a single transformation operation
type AdapterTransform struct {
	// Exactly one of these will be set
	Rename      *RenameTransform
	Default     *DefaultTransform
	Drop        *DropTransform
	Wrap        *WrapTransform
	Unwrap      *UnwrapTransform
	RenameParam *RenameParamTransform
}

// RenameTransform moves/renames a field by JSON Pointer
type RenameTransform struct {
	From string // JSON Pointer to source field
	To   string // JSON Pointer to destination field
}

// DefaultTransform sets a default value for a missing field
type DefaultTransform struct {
	Pointer string // JSON Pointer to the field
	Value   any    // Default value
}

// DropTransform removes a field
type DropTransform struct {
	Pointer string // JSON Pointer to the field to remove
}

// WrapTransform wraps a value in an envelope
type WrapTransform struct {
	Pointer  string // JSON Pointer to the location to wrap
	Envelope string // Name of the wrapper field
}

// UnwrapTransform unwraps a value from an envelope
type UnwrapTransform struct {
	Pointer  string // JSON Pointer to the wrapped value
	Envelope string // Name of the wrapper field to remove
}

// RenameParamTransform renames a query or header parameter
type RenameParamTransform struct {
	From     string // Current parameter name
	To       string // New parameter name
	Location string // "query" or "header"
}

// ApplyRequestTransforms applies all request transforms at stage 8
// Per Phase 8.2: transforms are applied before re-validation against target schema
// Returns an error if a transform fails (500 - adapter failure, never 400)
func ApplyRequestTransforms(req *http.Request, body []byte, config *AdapterConfig) ([]byte, error) {
	if config == nil || len(config.RequestTransforms) == 0 {
		return body, nil
	}

	// Parse request body as JSON
	var data any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, fmt.Errorf("adapter: failed to parse request body as JSON: %w", err)
		}
	} else {
		data = map[string]any{}
	}

	// Apply each transform in sequence
	for i, transform := range config.RequestTransforms {
		var err error
		data, err = applyTransform(transform, data, true)
		if err != nil {
			return nil, fmt.Errorf("adapter: request transform %d failed: %w", i, err)
		}
	}

	// Handle parameter renames separately (they operate on HTTP request, not body)
	if err := applyRequestParamRenames(req, config.RequestTransforms); err != nil {
		return nil, fmt.Errorf("adapter: request parameter rename failed: %w", err)
	}

	// Serialize transformed body
	transformed, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("adapter: failed to serialize transformed request body: %w", err)
	}

	return transformed, nil
}

// ApplyResponseTransforms applies all response transforms at stage 2
// Per Phase 8.2: applied before scrubbing; structure-altering forces buffered path
func ApplyResponseTransforms(body []byte, config *AdapterConfig) ([]byte, error) {
	if config == nil || len(config.ResponseTransforms) == 0 {
		return body, nil
	}

	// Parse response body as JSON
	var data any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, fmt.Errorf("adapter: failed to parse response body as JSON: %w", err)
		}
	} else {
		data = map[string]any{}
	}

	// Apply each transform in sequence
	for i, transform := range config.ResponseTransforms {
		var err error
		data, err = applyTransform(transform, data, false)
		if err != nil {
			return nil, fmt.Errorf("adapter: response transform %d failed: %w", i, err)
		}
	}

	// Serialize transformed body
	transformed, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("adapter: failed to serialize transformed response body: %w", err)
	}

	return transformed, nil
}

// applyTransform applies a single transform to the data
func applyTransform(transform AdapterTransform, data any, isRequest bool) (any, error) {
	switch {
	case transform.Rename != nil:
		return applyRenameTransform(data, transform.Rename)
	case transform.Default != nil:
		return applyDefaultTransform(data, transform.Default)
	case transform.Drop != nil:
		return applyDropTransform(data, transform.Drop)
	case transform.Wrap != nil:
		return applyWrapTransform(data, transform.Wrap)
	case transform.Unwrap != nil:
		return applyUnwrapTransform(data, transform.Unwrap)
	case transform.RenameParam != nil:
		// Parameter renames are handled separately in applyRequestParamRenames
		return data, nil
	default:
		return nil, fmt.Errorf("adapter: unknown transform type")
	}
}

// applyRenameTransform moves/renames a field by JSON Pointer
func applyRenameTransform(data any, transform *RenameTransform) (any, error) {
	// Resolve source pointer; a rename is a move, so a missing source is an
	// adapter failure rather than a silent no-op
	sourceValue, found, err := jsonPointerGet(data, transform.From)
	if err != nil {
		return nil, fmt.Errorf("rename: failed to resolve source pointer %q: %w", transform.From, err)
	}
	if !found {
		return nil, fmt.Errorf("rename: source pointer %q does not exist", transform.From)
	}

	// Resolve destination pointer parent (for in-place modification)
	destParent, destKey, err := getParentAndKey(data, transform.To)
	if err != nil {
		return nil, fmt.Errorf("rename: failed to resolve destination pointer %q: %w", transform.To, err)
	}

	// Resolve the source parent so the original can be removed. Both
	// resolutions happen before either mutation, so a failure leaves the
	// body unmodified rather than half-moved.
	sourceParent, sourceKey, err := getParentAndKey(data, transform.From)
	if err != nil {
		return nil, fmt.Errorf("rename: failed to resolve source pointer %q: %w", transform.From, err)
	}

	destMap, ok := destParent.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("rename: destination parent of %q is not an object", transform.To)
	}
	sourceMap, ok := sourceParent.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("rename: source parent of %q is not an object", transform.From)
	}

	destMap[destKey] = sourceValue

	// Remove the source, unless the rename names its own location — writing
	// the destination first means deleting then would drop the value
	if transform.From != transform.To {
		delete(sourceMap, sourceKey)
	}

	return data, nil
}

// applyDefaultTransform sets a default value if the field is missing
func applyDefaultTransform(data any, transform *DefaultTransform) (any, error) {
	// Try to resolve the pointer
	_, found, err := jsonPointerGet(data, transform.Pointer)
	if err != nil {
		return nil, fmt.Errorf("default: failed to resolve pointer %q: %w", transform.Pointer, err)
	}

	// If field exists (including an explicit null), do nothing
	if found {
		return data, nil
	}

	// Field doesn't exist, set default value
	parent, key, err := getParentAndKey(data, transform.Pointer)
	if err != nil {
		return nil, fmt.Errorf("default: failed to resolve pointer %q: %w", transform.Pointer, err)
	}

	if parentMap, ok := parent.(map[string]any); ok {
		parentMap[key] = transform.Value
	} else {
		return nil, fmt.Errorf("default: parent of %q is not an object", transform.Pointer)
	}

	return data, nil
}

// applyDropTransform removes a field
func applyDropTransform(data any, transform *DropTransform) (any, error) {
	// Resolve parent and key
	parent, key, err := getParentAndKey(data, transform.Pointer)
	if err != nil {
		// If field doesn't exist, drop is a no-op
		return data, nil
	}

	if parentMap, ok := parent.(map[string]any); ok {
		delete(parentMap, key)
	} else {
		return nil, fmt.Errorf("drop: parent is not an object")
	}

	return data, nil
}

// applyWrapTransform wraps a value in an envelope
func applyWrapTransform(data any, transform *WrapTransform) (any, error) {
	// Resolve the value to wrap
	value, _, err := jsonPointerGet(data, transform.Pointer)
	if err != nil {
		return nil, fmt.Errorf("wrap: failed to resolve pointer %q: %w", transform.Pointer, err)
	}

	// Create envelope: {transform.Envelope: value}
	envelope := map[string]any{
		transform.Envelope: value,
	}

	// Replace the value with the envelope
	parent, key, err := getParentAndKey(data, transform.Pointer)
	if err != nil {
		return nil, fmt.Errorf("wrap: failed to resolve parent for %q: %w", transform.Pointer, err)
	}

	if parentMap, ok := parent.(map[string]any); ok {
		parentMap[key] = envelope
	} else {
		return nil, fmt.Errorf("wrap: parent is not an object")
	}

	return data, nil
}

// applyUnwrapTransform unwraps a value from an envelope
func applyUnwrapTransform(data any, transform *UnwrapTransform) (any, error) {
	// Resolve the envelope
	envelopeValue, _, err := jsonPointerGet(data, transform.Pointer)
	if err != nil {
		return nil, fmt.Errorf("unwrap: failed to resolve pointer %q: %w", transform.Pointer, err)
	}

	envelope, ok := envelopeValue.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unwrap: value at %q is not an object", transform.Pointer)
	}

	// Extract the unwrapped value
	unwrapped, exists := envelope[transform.Envelope]
	if !exists {
		return nil, fmt.Errorf("unwrap: envelope field %q not found", transform.Envelope)
	}

	// Replace the envelope with the unwrapped value
	parent, key, err := getParentAndKey(data, transform.Pointer)
	if err != nil {
		return nil, fmt.Errorf("unwrap: failed to resolve parent for %q: %w", transform.Pointer, err)
	}

	if parentMap, ok := parent.(map[string]any); ok {
		parentMap[key] = unwrapped
	} else {
		return nil, fmt.Errorf("unwrap: parent is not an object")
	}

	return data, nil
}

// applyRequestParamRenames applies parameter rename transforms to the HTTP request
func applyRequestParamRenames(req *http.Request, transforms []AdapterTransform) error {
	for _, transform := range transforms {
		if transform.RenameParam == nil {
			continue
		}

		param := transform.RenameParam
		switch param.Location {
		case "query":
			if err := renameQueryParam(req, param.From, param.To); err != nil {
				return fmt.Errorf("renameParam: query rename failed: %w", err)
			}
		case "header":
			renameHeaderParam(req, param.From, param.To)
		default:
			return fmt.Errorf("renameParam: unknown location %q", param.Location)
		}
	}
	return nil
}

// renameQueryParam renames a query parameter
func renameQueryParam(req *http.Request, from, to string) error {
	query := req.URL.Query()

	// Check if source parameter exists
	values := query[from]
	if len(values) == 0 {
		// Source doesn't exist, nothing to rename
		return nil
	}

	// Remove old parameter
	query.Del(from)

	// Add with new name
	for _, v := range values {
		query.Add(to, v)
	}

	req.URL.RawQuery = query.Encode()
	return nil
}

// renameHeaderParam renames a header (case-insensitive)
func renameHeaderParam(req *http.Request, from, to string) {
	// Get all values for the source header
	values := req.Header.Values(from)
	if len(values) == 0 {
		// Source doesn't exist, nothing to rename
		return
	}

	// Remove old header (case-insensitive)
	deleteHeaderFold(req.Header, from)

	// Add with new name
	for _, v := range values {
		req.Header.Add(to, v)
	}
}

// getParentAndKey resolves the parent object and final key for a JSON Pointer
// This is a helper for in-place modifications
func getParentAndKey(data any, pointer string) (any, string, error) {
	if pointer == "" || pointer == "/" {
		return nil, "", fmt.Errorf("invalid pointer: %q", pointer)
	}

	// Split the pointer into parent path and final key
	parts := strings.Split(pointer, "/")
	if len(parts) < 2 {
		return nil, "", fmt.Errorf("invalid pointer format: %q", pointer)
	}

	// Get parent path (everything except the last component)
	parentPath := "/" + strings.Join(parts[1:len(parts)-1], "/")
	finalKey := parts[len(parts)-1]

	// Resolve parent
	if parentPath == "/" {
		return data, finalKey, nil
	}

	parent, _, err := jsonPointerGet(data, parentPath)
	if err != nil {
		return nil, "", err
	}

	return parent, finalKey, nil
}

// RequiresBufferedResponse checks if any response transform requires buffering
// Structure-altering transforms (wrap, unwrap) require the full response body
func RequiresBufferedResponse(config *AdapterConfig) bool {
	if config == nil {
		return false
	}

	for _, transform := range config.ResponseTransforms {
		if transform.Wrap != nil || transform.Unwrap != nil {
			return true
		}
	}

	return false
}

// ParseAdapterTransforms parses x-adapter transforms from fragment metadata
func ParseAdapterTransforms(adapterConfig map[string]any) (*AdapterConfig, error) {
	config := &AdapterConfig{}

	// Extract targetVersion
	targetVersion, ok := adapterConfig["targetVersion"].(string)
	if !ok {
		return nil, fmt.Errorf("adapter: targetVersion is required")
	}
	config.TargetVersion = targetVersion

	// Parse request transforms
	requestTransforms, ok := adapterConfig["request"].([]any)
	if !ok {
		return nil, fmt.Errorf("adapter: request transforms must be an array")
	}

	for i, raw := range requestTransforms {
		transform, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("adapter: request transform %d must be an object", i)
		}

		parsed, err := parseTransform(transform)
		if err != nil {
			return nil, fmt.Errorf("adapter: request transform %d: %w", i, err)
		}
		config.RequestTransforms = append(config.RequestTransforms, *parsed)
	}

	// Parse response transforms
	responseTransforms, ok := adapterConfig["response"].([]any)
	if !ok {
		return nil, fmt.Errorf("adapter: response transforms must be an array")
	}

	for i, raw := range responseTransforms {
		transform, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("adapter: response transform %d must be an object", i)
		}

		parsed, err := parseTransform(transform)
		if err != nil {
			return nil, fmt.Errorf("adapter: response transform %d: %w", i, err)
		}
		config.ResponseTransforms = append(config.ResponseTransforms, *parsed)
	}

	return config, nil
}

// parseTransform parses a single transform from a map
func parseTransform(transform map[string]any) (*AdapterTransform, error) {
	result := &AdapterTransform{}

	// Check for each transform type
	if rename, ok := transform["rename"].(map[string]any); ok {
		from, fromOk := rename["from"].(string)
		to, toOk := rename["to"].(string)
		if !fromOk || !toOk {
			return nil, fmt.Errorf("rename transform requires from and to strings")
		}
		result.Rename = &RenameTransform{From: from, To: to}
		return result, nil
	}

	if defaultOp, ok := transform["default"].(map[string]any); ok {
		pointer, pointerOk := defaultOp["pointer"].(string)
		value, hasValue := defaultOp["value"]
		if !pointerOk || !hasValue {
			return nil, fmt.Errorf("default transform requires pointer and value")
		}
		result.Default = &DefaultTransform{Pointer: pointer, Value: value}
		return result, nil
	}

	if drop, ok := transform["drop"].(string); ok {
		result.Drop = &DropTransform{Pointer: drop}
		return result, nil
	}

	if wrap, ok := transform["wrap"].(map[string]any); ok {
		pointer, pointerOk := wrap["pointer"].(string)
		envelope, envelopeOk := wrap["envelope"].(string)
		if !pointerOk || !envelopeOk {
			return nil, fmt.Errorf("wrap transform requires pointer and envelope")
		}
		result.Wrap = &WrapTransform{Pointer: pointer, Envelope: envelope}
		return result, nil
	}

	if unwrap, ok := transform["unwrap"].(map[string]any); ok {
		pointer, pointerOk := unwrap["pointer"].(string)
		envelope, envelopeOk := unwrap["envelope"].(string)
		if !pointerOk || !envelopeOk {
			return nil, fmt.Errorf("unwrap transform requires pointer and envelope")
		}
		result.Unwrap = &UnwrapTransform{Pointer: pointer, Envelope: envelope}
		return result, nil
	}

	if renameParam, ok := transform["renameParam"].(map[string]any); ok {
		from, fromOk := renameParam["from"].(string)
		to, toOk := renameParam["to"].(string)
		location, locationOk := renameParam["location"].(string)
		if !fromOk || !toOk || !locationOk {
			return nil, fmt.Errorf("renameParam transform requires from, to, and location")
		}
		result.RenameParam = &RenameParamTransform{From: from, To: to, Location: location}
		return result, nil
	}

	return nil, fmt.Errorf("unknown transform type")
}

// ApplyResponseTransformsToReader applies response transforms to a response body reader
// This is used when buffering the response for transformation
func ApplyResponseTransformsToReader(reader io.Reader, config *AdapterConfig) (io.Reader, error) {
	if config == nil || len(config.ResponseTransforms) == 0 {
		return reader, nil
	}

	// Read the entire body
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("adapter: failed to read response body: %w", err)
	}

	// Apply transforms
	transformed, err := ApplyResponseTransforms(body, config)
	if err != nil {
		return nil, err
	}

	// Return a new reader with the transformed body
	return &transformingReader{data: transformed}, nil
}

// transformingReader wraps a byte slice in an io.Reader interface
type transformingReader struct {
	data   []byte
	offset int
}

func (r *transformingReader) Read(p []byte) (n int, err error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}
