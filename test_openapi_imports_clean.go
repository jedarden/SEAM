package main

import (
	"fmt"
	_ "github.com/getkin/kin-openapi/openapi3"
	_ "github.com/go-openapi/runtime/server-middleware/docui"
	_ "github.com/mvrilo/go-redoc"
	_ "github.com/oaswrap/openapi-ui"
	_ "github.com/pb33f/libopenapi"
)

func main() {
	fmt.Println("✅ All OpenAPI libraries imported successfully!")
	fmt.Println("Go version compatibility: 1.25.7")
	fmt.Println("Tested libraries:")
	fmt.Println("  - getkin/kin-openapi (OpenAPI 3.1 parser)")
	fmt.Println("  - go-openapi/runtime/server-middleware (Swagger UI)")
	fmt.Println("  - mvrilo/go-redoc (ReDoc UI)")
	fmt.Println("  - oaswrap/openapi-ui (Alternative UI)")
	fmt.Println("  - pb33f/libopenapi (Comprehensive OpenAPI toolkit)")
}
