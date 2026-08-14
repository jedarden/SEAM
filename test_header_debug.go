package main

import (
	"fmt"
	"net/http/httptest"
)

func main() {
	// Create a request with various header formats
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-SEAM-Spec-Version", "v1.0")
	req.Header.Set("x-seam-api-version", "v2.0")
	req.Header.Set("X-SEAM-FAKE", "fake")

	// Print what headers actually look like
	fmt.Println("Headers in request:")
	for name, values := range req.Header {
		fmt.Printf("  %s: %v\n", name, values)
	}

	// Test prefix matching
	fmt.Println("\nPrefix matching:")
	for name := range req.Header {
		fmt.Printf("  %s starts with X-SEAM-: %v\n", name, len(name) >= 7 && name[:7] == "X-SEAM-")
		if len(name) >= 7 && name[:7] == "X-SEAM-" {
			fmt.Printf("    -> matches X-SEAM-Spec-Version: %v\n", name == "X-SEAM-Spec-Version")
			fmt.Printf("    -> matches X-SEAM-API-Version: %v\n", name == "X-SEAM-API-Version")
		}
	}
}
