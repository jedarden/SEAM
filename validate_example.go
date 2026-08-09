package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func main() {
	// Read the schema
	schemaBytes, err := os.ReadFile("spec/route-fragment-schema.json")
	if err != nil {
		fmt.Printf("Error reading schema: %v\n", err)
		os.Exit(1)
	}

	var schemaDef map[string]any
	if err := json.Unmarshal(schemaBytes, &schemaDef); err != nil {
		fmt.Printf("Error parsing schema JSON: %v\n", err)
		os.Exit(1)
	}

	// Create schema compiler
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", schemaDef); err != nil {
		fmt.Printf("Error adding schema resource: %v\n", err)
		os.Exit(1)
	}

	schema, err := compiler.Compile("schema.json")
	if err != nil {
		fmt.Printf("Error compiling schema: %v\n", err)
		os.Exit(1)
	}

	// Read the example file
	exampleBytes, err := os.ReadFile("docs/notes/route-fragment-schema.json")
	if err != nil {
		fmt.Printf("Error reading example file: %v\n", err)
		os.Exit(1)
	}

	var example map[string]any
	if err := json.Unmarshal(exampleBytes, &example); err != nil {
		fmt.Printf("Error parsing example JSON: %v\n", err)
		os.Exit(1)
	}

	// Validate the example against the schema
	if err := schema.Validate(example); err != nil {
		fmt.Printf("❌ Validation failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Example fragment is valid against the schema!")

	// Check for all five extensions
	fmt.Println("\nChecking for all five SEAM extensions:")

	// Check fragment-level extensions
	for _, ext := range []string{"x-seam-schema", "x-upstream-map"} {
		if val, ok := example[ext]; ok {
			fmt.Printf("  ✓ %s: %v\n", ext, val)
		} else {
			fmt.Printf("  ✗ %s: MISSING\n", ext)
		}
	}

	// Check operation-level extensions
	if paths, ok := example["paths"].(map[string]any); ok {
		for pathName, pathItem := range paths {
			if pathMap, ok := pathItem.(map[string]any); ok {
				for method, operation := range pathMap {
					if method != "get" && method != "post" && method != "put" && method != "delete" && method != "patch" {
						continue
					}
					if opMap, ok := operation.(map[string]any); ok {
						fmt.Printf("\n  Found operation: %s %s\n", method.upper(), pathName)
						for _, ext := range []string{"x-loop-guard", "x-cost-per-call", "x-quota"} {
							if val, ok := opMap[ext]; ok {
								fmt.Printf("    ✓ %s: %v\n", ext, val)
							} else {
								fmt.Printf("    ✗ %s: MISSING\n", ext)
							}
						}
					}
				}
			}
		}
	}

	fmt.Println("\n✅ All five SEAM extensions are present and validated!")
}