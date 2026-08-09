package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func main() {
	// Test 1: Load and compile the base schema
	fmt.Println("=== Test 1: Load base schema ===")
	schemaBytes, err := os.ReadFile("docs/notes/base-fragment-schema.json")
	if err != nil {
		fmt.Printf("❌ Failed to read schema: %v\n", err)
		os.Exit(1)
	}

	// Create schema compiler
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://seam.ardenone.com/schemas/base-fragment-schema.json", bytes.NewReader(schemaBytes)); err != nil {
		fmt.Printf("❌ Failed to add schema to compiler: %v\n", err)
		os.Exit(1)
	}

	schema, err := compiler.Compile("https://seam.ardenone.com/schemas/base-fragment-schema.json")
	if err != nil {
		fmt.Printf("❌ Failed to compile schema: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ Base schema compiled successfully")

	// Test 2: Validate minimal fragment (paths only, no openapi field)
	fmt.Println("\n=== Test 2: Validate minimal fragment ===")
	minimalFragment := map[string]interface{}{
		"paths": map[string]interface{}{
			"/health": map[string]interface{}{
				"get": map[string]interface{}{
					"summary": "Health check",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "OK",
						},
					},
				},
			},
		},
	}

	if err := validateFragment(schema, minimalFragment, "minimal fragment"); err != nil {
		fmt.Printf("❌ Validation failed: %v\n", err)
		os.Exit(1)
	}

	// Test 3: Validate fragment with openapi field
	fmt.Println("\n=== Test 3: Validate fragment with openapi field ===")
	fragmentWithVersion := map[string]interface{}{
		"openapi": "3.1.0",
		"paths": map[string]interface{}{
			"/users": map[string]interface{}{
				"get": map[string]interface{}{
					"summary":  "List users",
					"operationId": "listUsers",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Success",
						},
					},
				},
			},
		},
	}

	if err := validateFragment(schema, fragmentWithVersion, "fragment with openapi field"); err != nil {
		fmt.Printf("❌ Validation failed: %v\n", err)
		os.Exit(1)
	}

	// Test 4: Validate fragment with components
	fmt.Println("\n=== Test 4: Validate fragment with components ===")
	fragmentWithComponents := map[string]interface{}{
		"openapi": "3.1.0",
		"paths": map[string]interface{}{
			"/users": map[string]interface{}{
				"get": map[string]interface{}{
					"summary": "List users",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Success",
							"content": map[string]interface{}{
								"application/json": map[string]interface{}{
									"schema": map[string]interface{}{
										"$ref": "#/components/schemas/UserList",
									},
								},
							},
						},
					},
				},
			},
		},
		"components": map[string]interface{}{
			"schemas": map[string]interface{}{
				"UserList": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"id":    map[string]interface{}{"type": "string"},
							"name":  map[string]interface{}{"type": "string"},
						},
					},
				},
			},
		},
	}

	if err := validateFragment(schema, fragmentWithComponents, "fragment with components"); err != nil {
		fmt.Printf("❌ Validation failed: %v\n", err)
		os.Exit(1)
	}

	// Test 5: Validate all HTTP methods
	fmt.Println("\n=== Test 5: Validate all standard HTTP methods ===")
	allMethodsFragment := map[string]interface{}{
		"openapi": "3.1.0",
		"paths": map[string]interface{}{
			"/resource": map[string]interface{}{
				"get":     map[string]interface{}{"responses": map[string]interface{}{"200": map[string]interface{}{"description": "OK"}}},
				"post":    map[string]interface{}{"responses": map[string]interface{}{"201": map[string]interface{}{"description": "Created"}}},
				"put":     map[string]interface{}{"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Updated"}}},
				"delete":  map[string]interface{}{"responses": map[string]interface{}{"204": map[string]interface{}{"description": "Deleted"}}},
				"patch":   map[string]interface{}{"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Patched"}}},
				"options": map[string]interface{}{"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Options"}}},
				"head":    map[string]interface{}{"responses": map[string]interface{}{"200": map[string]interface{}{"description": "Head"}}},
			},
		},
	}

	if err := validateFragment(schema, allMethodsFragment, "fragment with all HTTP methods"); err != nil {
		fmt.Printf("❌ Validation failed: %v\n", err)
		os.Exit(1)
	}

	// Test 6: Validate rejection of missing paths
	fmt.Println("\n=== Test 6: Validate rejection of missing paths ===")
	invalidFragment := map[string]interface{}{
		"openapi": "3.1.0",
	}

	if err := validateFragmentExpectError(schema, invalidFragment, "fragment without paths"); err != nil {
		fmt.Printf("❌ Expected rejection failed: %v\n", err)
		os.Exit(1)
	}

	// Test 7: Validate rejection of invalid openapi version
	fmt.Println("\n=== Test 7: Validate rejection of invalid openapi version ===")
	invalidVersionFragment := map[string]interface{}{
		"openapi": "3.0.0",
		"paths": map[string]interface{}{
			"/health": map[string]interface{}{
				"get": map[string]interface{}{
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "OK"},
					},
				},
			},
		},
	}

	// This should pass since openapi is informational and the schema just validates pattern if present
	// The pattern requires 3.1.x, so 3.0.0 should fail
	if err := validateFragmentExpectError(schema, invalidVersionFragment, "fragment with invalid openapi version"); err != nil {
		fmt.Printf("❌ Expected rejection failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n=== ✅ All validation tests passed ===")
	fmt.Println("\nSummary:")
	fmt.Println("✅ Base schema compiles correctly")
	fmt.Println("✅ Minimal fragments validate (paths only)")
	fmt.Println("✅ Fragments with openapi field validate")
	fmt.Println("✅ Fragments with components validate")
	fmt.Println("✅ All standard HTTP methods supported (GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD)")
	fmt.Println("✅ Invalid fragments correctly rejected")
}

func validateFragment(schema *jsonschema.Schema, fragment map[string]interface{}, name string) error {
	fragmentJSON, err := json.Marshal(fragment)
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", name, err)
	}

	var iface interface{}
	if err := json.Unmarshal(fragmentJSON, &iface); err != nil {
		return fmt.Errorf("failed to unmarshal %s: %w", name, err)
	}

	if err := schema.Validate(interface{}(iface)); err != nil {
		return fmt.Errorf("%s validation failed: %w", name, err)
	}

	fmt.Printf("✅ %s validated successfully\n", name)
	return nil
}

func validateFragmentExpectError(schema *jsonschema.Schema, fragment map[string]interface{}, name string) error {
	fragmentJSON, err := json.Marshal(fragment)
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", name, err)
	}

	var iface interface{}
	if err := json.Unmarshal(fragmentJSON, &iface); err != nil {
		return fmt.Errorf("failed to unmarshal %s: %w", name, err)
	}

	if err := schema.Validate(interface{}(iface)); err == nil {
		return fmt.Errorf("%s should have failed validation but passed", name)
	} else {
		fmt.Printf("✅ %s correctly rejected: %v\n", name, err)
		return nil
	}
}
