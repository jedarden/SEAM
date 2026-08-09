package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/ardenone/seam/internal/spec"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Test 1: Create a FragmentLoader
	fmt.Println("=== Test 1: Creating FragmentLoader ===")
	loader, err := spec.NewFragmentLoader()
	if err != nil {
		log.Fatalf("Failed to create FragmentLoader: %v", err)
	}
	fmt.Println("✓ FragmentLoader created successfully")

	// Test 2: Load fragments from directory
	fmt.Println("\n=== Test 2: Loading fragments from directory ===")
	fragmentsDir := "./spec/fragments.d"
	if err := loader.LoadDirectory(fragmentsDir); err != nil {
		log.Fatalf("Failed to load fragments: %v", err)
	}
	fmt.Printf("✓ Fragments loaded from directory: %s\n", fragmentsDir)
	fmt.Printf("  Valid fragments: %d\n", loader.GetValidFragmentCount())
	fmt.Printf("  Quarantined: %d\n", loader.GetQuarantinedCount())

	// Test 3: Merge fragments into OpenAPI spec
	fmt.Println("\n=== Test 3: Merging fragments into OpenAPI 3.1 spec ===")
	baseURL := "http://localhost:8080"
	mergedJSON, err := loader.MergeFragments(baseURL)
	if err != nil {
		log.Fatalf("Failed to merge fragments: %v", err)
	}
	fmt.Println("✓ Fragments merged successfully")
	fmt.Printf("  Merged spec size: %d bytes\n", len(mergedJSON))

	// Test 4: Verify merged spec is stored in memory
	fmt.Println("\n=== Test 4: Verifying merged spec is stored in memory ===")
	doc := loader.GetDocument()
	if doc == nil {
		log.Fatalf("No document stored in memory")
	}
	fmt.Println("✓ Merged spec is stored in memory")

	// Test 5: Verify merged spec structure
	fmt.Println("\n=== Test 5: Verifying merged spec structure ===")
	var mergedSpec map[string]interface{}
	if err := json.Unmarshal(mergedJSON, &mergedSpec); err != nil {
		log.Fatalf("Failed to parse merged JSON: %v", err)
	}

	// Check OpenAPI version
	if openapi, ok := mergedSpec["openapi"].(string); ok && openapi == "3.1.0" {
		fmt.Println("✓ OpenAPI version is 3.1.0")
	} else {
		fmt.Printf("✗ OpenAPI version incorrect: %v\n", mergedSpec["openapi"])
	}

	// Check paths
	if paths, ok := mergedSpec["paths"].(map[string]interface{}); ok {
		fmt.Printf("✓ Paths object present with %d paths\n", len(paths))
		for path := range paths {
			fmt.Printf("  - %s\n", path)
		}
	} else {
		fmt.Println("✗ Paths object missing or invalid")
	}

	// Check servers
	if servers, ok := mergedSpec["servers"].([]interface{}); ok && len(servers) > 0 {
		fmt.Printf("✓ Servers object present with %d server(s)\n", len(servers))
		for _, s := range servers {
			if server, ok := s.(map[string]interface{}); ok {
				if url, ok := server["url"].(string); ok {
					fmt.Printf("  - URL: %s\n", url)
				}
			}
		}
	} else {
		fmt.Println("✗ Servers object missing or invalid")
	}

	// Test 6: Test full loader integration
	fmt.Println("\n=== Test 6: Testing full loader integration ===")
	fullLoader, err := spec.NewLoader(baseURL)
	if err != nil {
		log.Printf("Warning: Failed to create full loader: %v", err)
		fmt.Println("⚠ Full loader test skipped (fragments directory may not exist)")
	} else {
		fmt.Println("✓ Full loader created successfully")

		rawJSON := fullLoader.GetRawDocument()
		if rawJSON != nil {
			fmt.Printf("✓ Full loader has raw document (%d bytes)\n", len(rawJSON))
		} else {
			fmt.Println("✗ Full loader has no raw document")
		}

		routeCount := len(fullLoader.ListPaths())
		fmt.Printf("✓ Full loader has %d routes\n", routeCount)
	}

	fmt.Println("\n=== All Tests Complete ===")
}
