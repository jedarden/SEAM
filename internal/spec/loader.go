package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi-validator"
	"github.com/pb33f/libopenapi-validator/errors"
)

// Loader handles loading and serving OpenAPI specs
type Loader struct {
	specPath    string
	baseURL     string
	rawDocument []byte
	specVersion string // Stable hash of the spec
	loadedDoc   libopenapi.Document
	model       *libopenapi.DocumentModel[v3.Document]
	validator   validator.Validator
}

// New creates a new spec loader
func New(specDir, baseURL string) (*Loader, error) {
	// Expect exactly one spec file in the directory
	specPath := filepath.Join(specDir, "openapi.yaml")
	if _, err := os.Stat(specPath); err != nil {
		return nil, fmt.Errorf("spec file not found: %s: %w", specPath, err)
	}

	// Read the raw spec content
	rawDocument, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read spec file: %w", err)
	}

	// Compute stable hash of the spec for X-SEAM-Spec-Version
	hash := sha256.Sum256(rawDocument)
	specVersion := hex.EncodeToString(hash[:])[:16] // Use first 16 hex chars (64 bits)

	// Load the document using libopenapi
	loadedDoc, err := libopenapi.NewDocument(rawDocument)
	if err != nil {
		return nil, fmt.Errorf("failed to load OpenAPI document: %w", err)
	}

	// Build the model for validation and route extraction
	documentModel, err := loadedDoc.BuildV3Model()
	if err != nil {
		return nil, fmt.Errorf("failed to build OpenAPI model: %w", err)
	}

	// Store the document model for later use
	model := documentModel

	// Create the validator using the v3 model directly
	// We don't pass any options, so it uses defaults
	// NOTE: This validator is initialized but not yet used for validation (Phase 1a)
	v := validator.NewValidatorFromV3Model(&model.Model, nil)

	return &Loader{
		specPath:    specPath,
		baseURL:     baseURL,
		rawDocument: rawDocument,
		specVersion: specVersion,
		loadedDoc:   loadedDoc,
		model:       model,
		validator:   v,
	}, nil
}

// GetVersion returns the stable spec version hash
func (l *Loader) GetVersion() string {
	return l.specVersion
}

// GetAPIVersion returns the API version (constant _unversioned in Phase 1a)
func (l *Loader) GetAPIVersion() string {
	return "_unversioned"
}

// GetRawJSON returns the spec as JSON with servers populated
func (l *Loader) GetRawJSON() ([]byte, error) {
	// Populate servers with the caller-facing base URL
	// Clear any existing servers and add our caller-facing one
	l.model.Model.Servers = nil
	server := &v3.Server{
		URL:         l.baseURL,
		Description: "SEAM caller-facing endpoint",
	}
	l.model.Model.Servers = []*v3.Server{server}

	// Serialize back to JSON
	docJSON, err := json.MarshalIndent(l.model.Model, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to serialize spec to JSON: %w", err)
	}

	return docJSON, nil
}

// GetRawDocument returns the original raw document bytes
func (l *Loader) GetRawDocument() []byte {
	return l.rawDocument
}

// ValidateRequest validates an HTTP request against the OpenAPI spec
// Returns nil if valid, or a ValidationError if invalid
func (l *Loader) ValidateRequest(r *http.Request) *ValidationError {
	// Validate the request
	valid, validationErrors := l.validator.ValidateHttpRequest(r)

	// If valid, request is valid
	if valid {
		return nil
	}

	// Build structured error response
	return &ValidationError{
		Errors: validationErrors,
	}
}

// GetRoute returns the route information for a given path, method, and version
func (l *Loader) GetRoute(path, method, version string) (*RouteInfo, error) {
	// Find the path item - Get returns (value, present)
	pathItem, present := l.model.Model.Paths.PathItems.Get(path)
	if !present || pathItem == nil {
		return nil, fmt.Errorf("path not found: %s", path)
	}

	// Get the operation
	var operation *v3.Operation
	switch method {
	case http.MethodGet:
		operation = pathItem.Get
	case http.MethodPost:
		operation = pathItem.Post
	case http.MethodPut:
		operation = pathItem.Put
	case http.MethodDelete:
		operation = pathItem.Delete
	case http.MethodPatch:
		operation = pathItem.Patch
	case http.MethodHead:
		operation = pathItem.Head
	case http.MethodOptions:
		operation = pathItem.Options
	case http.MethodTrace:
		operation = pathItem.Trace
	case "":
		// No method specified - return info for all methods
		return l.getAllMethodsForPath(pathItem, path)
	default:
		return nil, fmt.Errorf("method not found: %s", method)
	}

	if operation == nil {
		return nil, fmt.Errorf("method %s not defined for path: %s", method, path)
	}

	return &RouteInfo{
		Path:      path,
		Method:    method,
		Version:   version,
		Operation: operation,
		PathItem:  pathItem,
	}, nil
}

// getAllMethodsForPath returns route information for all methods on a path
func (l *Loader) getAllMethodsForPath(pathItem *v3.PathItem, path string) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:     path,
		Method:   "",
		Version:  "_unversioned",
		PathItem: pathItem,
	}

	methods := []string{}

	if pathItem.Get != nil {
		methods = append(methods, http.MethodGet)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodGet, Operation: pathItem.Get})
	}
	if pathItem.Post != nil {
		methods = append(methods, http.MethodPost)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodPost, Operation: pathItem.Post})
	}
	if pathItem.Put != nil {
		methods = append(methods, http.MethodPut)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodPut, Operation: pathItem.Put})
	}
	if pathItem.Delete != nil {
		methods = append(methods, http.MethodDelete)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodDelete, Operation: pathItem.Delete})
	}
	if pathItem.Patch != nil {
		methods = append(methods, http.MethodPatch)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodPatch, Operation: pathItem.Patch})
	}
	if pathItem.Head != nil {
		methods = append(methods, http.MethodHead)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodHead, Operation: pathItem.Head})
	}
	if pathItem.Options != nil {
		methods = append(methods, http.MethodOptions)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodOptions, Operation: pathItem.Options})
	}
	if pathItem.Trace != nil {
		methods = append(methods, http.MethodTrace)
		route.Operations = append(route.Operations, &OperationHolder{Method: http.MethodTrace, Operation: pathItem.Trace})
	}

	route.AvailableMethods = methods
	return route, nil
}

// ListPaths returns all paths defined in the spec
func (l *Loader) ListPaths() []string {
	paths := []string{}
	// Use range iteration over the iterator from FromOldest
	for path, _ := range l.model.Model.Paths.PathItems.FromOldest() {
		paths = append(paths, path)
	}
	return paths
}

// ValidationError represents a structured validation error
type ValidationError struct {
	Errors []*errors.ValidationError
}

// ValidationErrorItem represents a single validation error from libopenapi-validator
type ValidationErrorItem struct {
	Message           string `json:"message"`
	Reason            string `json:"reason"`
	ValidationType    string `json:"validationType"`
	ValidationSubType string `json:"validationSubType"`
	SpecLine          int    `json:"specLine"`
	SpecCol           int    `json:"specColumn"`
	HowToFix          string `json:"howToFix"`
	RequestPath       string `json:"requestPath"`
	SpecPath          string `json:"specPath"`
}

// ToJSON converts the validation error to a structured JSON response
func (ve *ValidationError) ToJSON(path, method string) map[string]interface{} {
	errorDetails := []map[string]interface{}{}

	for _, err := range ve.Errors {
		errorDetails = append(errorDetails, map[string]interface{}{
			"field":       err.SpecPath,
			"expected":    err.HowToFix,
			"actual":      err.RequestPath,
			"reason":      err.Reason,
			"line":        err.SpecLine,
			"column":      err.SpecCol,
		})
	}

	return map[string]interface{}{
		"error":             "validation_failed",
		"message":          "Request does not conform to the OpenAPI specification",
		"validation_errors": errorDetails,
		"docs_pointer": fmt.Sprintf("/docs/route?path=%s&method=%s&version=_unversioned", path, method),
	}
}

// RouteInfo contains information about a specific route
type RouteInfo struct {
	Path             string
	Method           string
	Version          string
	Operation        *v3.Operation
	PathItem         *v3.PathItem
	AvailableMethods []string
	Operations       []*OperationHolder
}

// OperationHolder holds an operation with its HTTP method
type OperationHolder struct {
	Method    string
	Operation *v3.Operation
}
