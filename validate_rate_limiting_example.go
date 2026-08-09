package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func main() {
	log.SetFlags(log.Lshortfile)
	log.SetPrefix("[VALIDATE] ")

	// Read schema
	schemaBytes, err := os.ReadFile("spec/route-fragment-schema.json")
	if err != nil {
		log.Fatalf("Failed to read schema: %v", err)
	}

	var schemaDef map[string]any
	if err := json.Unmarshal(schemaBytes, &schemaDef); err != nil {
		log.Fatalf("Failed to parse schema JSON: %v", err)
	}

	// Compile schema
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", schemaDef); err != nil {
		log.Fatalf("Failed to add schema resource: %v", err)
	}

	schema, err := compiler.Compile("schema.json")
	if err != nil {
		log.Fatalf("Failed to compile schema: %v", err)
	}

	log.Printf("Schema compiled successfully")

	// Read example
	exampleBytes, err := os.ReadFile("examples/rate-limiting-monitoring.json")
	if err != nil {
		log.Fatalf("Failed to read example: %v", err)
	}

	var example map[string]any
	if err := json.Unmarshal(exampleBytes, &example); err != nil {
		log.Fatalf("Failed to parse example JSON: %v", err)
	}

	log.Printf("Validating example against schema...")

	// Validate
	if err := schema.Validate(example); err != nil {
		log.Printf("❌ VALIDATION FAILED")
		log.Printf("Error: %v", err)
		os.Exit(1)
	}

	log.Printf("✅ VALIDATION PASSED")
	fmt.Println()
	fmt.Println("Example fragment successfully validates against schema:")
	fmt.Println("  ✅ x-seam-schema: v1")
	fmt.Println("  ✅ x-loop-guard: max_depth, max_redirects")
	fmt.Println("  ✅ x-cost-per-call: USD per call")
	fmt.Println("  ✅ x-quota: limit, window, scope")
	fmt.Println("  ✅ x-upstream-map: multi-instance routing")
	fmt.Println()
	fmt.Println("All rate limiting and monitoring extensions are properly configured!")
}
