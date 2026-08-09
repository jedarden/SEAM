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
	fmt.Println("All OpenAPI libraries imported successfully!")
}