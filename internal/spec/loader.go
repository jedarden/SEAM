package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel/high/v3"
)

// Loader handles loading and serving OpenAPI specs
type Loader struct {
	specPath    string
	baseURL     string
	rawDocument []byte
	specVersion string // Stable hash of the spec
	loadedDoc   libopenapi.Document
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

	return &Loader{
		specPath:    specPath,
		baseURL:     baseURL,
		rawDocument: rawDocument,
		specVersion: specVersion,
		loadedDoc:   loadedDoc,
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
	// Build the model to access and modify it
	model, err := l.loadedDoc.BuildV3Model()
	if err != nil {
		return nil, fmt.Errorf("failed to build OpenAPI model: %w", err)
	}

	// Populate servers with the caller-facing base URL
	// Clear any existing servers and add our caller-facing one
	model.Model.Servers = nil
	server := &v3.Server{
		URL:         l.baseURL,
		Description: "SEAM caller-facing endpoint",
	}
	model.Model.Servers = []*v3.Server{server}

	// Serialize back to JSON
	docJSON, err := json.MarshalIndent(model.Model, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to serialize spec to JSON: %w", err)
	}

	return docJSON, nil
}

// GetRawDocument returns the original raw document bytes
func (l *Loader) GetRawDocument() []byte {
	return l.rawDocument
}
