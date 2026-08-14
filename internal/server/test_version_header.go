package server

import (
    "fmt"
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestVersionHeaderDebug(t *testing.T) {
    cfg := &Config{
        CallerPort:   8080,
        OperatorPort: 8081,
        BaseURL:      "http://localhost:8080",
        SpecDir:      "../../spec",
    }
    
    s := New(cfg)
    
    // Get hash and version from loader
    fmt.Printf("Loader GetVersion(): '%s' (len=%d)\n", s.specLoader.GetVersion(), len(s.specLoader.GetVersion()))
    fmt.Printf("Loader GetHash(): '%s' (len=%d)\n", s.specLoader.GetHash(), len(s.specLoader.GetHash()))
    
    // Make request
    req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
    w := httptest.NewRecorder()
    
    s.callerMux.ServeHTTP(w, req)
    
    resp := w.Result()
    
    fmt.Println("\nResponse Headers:")
    for k, v := range resp.Header {
        fmt.Printf("  %s: %v\n", k, v)
    }
    
    fmt.Printf("\nX-Seam-Spec-Version from response: '%s' (len=%d)\n", 
        resp.Header.Get("X-Seam-Spec-Version"), 
        len(resp.Header.Get("X-Seam-Spec-Version")))
}
