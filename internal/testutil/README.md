# SEAM Test Rig

This directory contains testing utilities for SEAM integration tests.

## Components

### Stub Upstream Server (`stubupstream`)

A scriptable HTTP server that simulates various upstream service behaviors for testing SEAM's integration with external services.

#### Behaviors

The stub upstream can be configured to exhibit different behaviors:

- **`Echo`**: Echoes back any injected credential in an error response body (tests response scrubbing)
- **`401`**: Returns 401 Unauthorized (tests credential rotation self-heal)
- **`5xx`**: Returns 500 Internal Server Error (tests error handling)
- **`Timeout`**: Hangs until timeout or cancellation (tests timeout handling)
- **`Upgrade`**: Signals a protocol upgrade (tests upgrade handling)
- **`Oversized`**: Returns an oversized response body (tests streaming and buffering)
- **`TransportFault`**: Simulates transport-level failures (tests circuit breaker)
- **`Normal`**: Returns normal 200 OK response (baseline)

#### Usage Example

```go
import "github.com/ardenone/seam/internal/testutil/stubupstream"

// Start a stub upstream that echoes credentials
stub := stubupstream.New(stubupstream.Config{
    Addr:     "localhost:15820",
    Behavior: stubupstream.BehaviorEcho,
})
if err := stub.Start(); err != nil {
    log.Fatal(err)
}
defer stub.Stop(context.Background())

// Make requests to stub.URL()
// The stub will behave according to the configured behavior

// Change behavior dynamically
stub.SetBehavior(stubupstream.Behavior401)

// Inspect call log
calls := stub.GetCallLog()
for _, call := range calls {
    fmt.Printf("Request to %s with Authorization: %s\n", call.Path, call.AuthHeader)
}
```

#### Control API

The stub upstream exposes a control endpoint at `/_control`:

- **GET `/_control`**: Returns current status (behavior, fail count, call count)
- **POST `/_control`**: Update behavior or reset fail counter

```bash
# Get status
curl http://localhost:15820/_control

# Change behavior
curl -X POST -H "Content-Type: application/json" \
  -d '{"behavior": "401", "resetFailCount": true}' \
  http://localhost:15820/_control
```

### OpenBao Helper (`openbao`)

Utilities for managing a local OpenBao development instance for testing SEAM's secret injection and caching.

#### Features

- Start/stop OpenBao dev server programmatically
- Provision test secrets
- Simulate credential rotation
- Test connection helper
- Test skipping for CI environments without OpenBao

#### Usage Example

```go
import "github.com/ardenone/seam/internal/testutil/openbao"

// Method 1: Let the test helper manage the server lifecycle
func TestSecretInjection(t *testing.T) {
    server := openbao.ManageTestServer(t)
    client := server.Client()

    // Use the client to write/read secrets
    ctx := context.Background()
    err := client.WriteSecret(ctx, "seam/routes/test/token", map[string]interface{}{
        "token": "test-secret",
    })
    // Server is automatically stopped when test completes
}

// Method 2: Manual server management
func TestCredentialRotation(t *testing.T) {
    cfg := openbao.ServerConfig{
        DevToken:   "test-token",
        ListenAddr: "localhost:18200",
    }
    server, err := openbao.NewServer(cfg)
    if err != nil {
        t.Fatal(err)
    }
    defer server.Close()

    client := server.Client()

    // Rotate a credential to test 401 handling
    newSecret, err := server.RotateCredential(ctx, "seam/routes/test/token", "token")
    // ...
}

// Method 3: Connect to existing OpenBao instance
func TestWithExistingOpenBao(t *testing.T) {
    // Skip if OpenBao not available
    openbao.SkipIfNoOpenBao(t)

    client, err := openbao.NewClientForTesting()
    if err != nil {
        t.Fatal(err)
    }

    // Use client...
}
```

#### Environment Variables

- **`TEST_OPENBAO_ADDR`**: OpenBao server address (default: `http://localhost:8200`)
- **`TEST_OPENBAO_TOKEN`**: OpenBao dev token (default: `dev-root-token`)

## Integration Tests

The test rig enables comprehensive integration testing of SEAM's core behaviors:

### 1. Secret Injection and Scrubbing

Tests that SEAM:
- Strips caller-provided headers before injection
- Fetches secrets from OpenBao
- Injects credentials into upstream requests
- Scrubs any credential echoes from responses

```go
func TestIntegration_SecretInjectionAndScrubbing(t *testing.T)
```

### 2. Credential Rotation Self-Heal

Tests that SEAM:
- Detects 401 responses from upstream
- Invalidates cached secrets
- Refetches from OpenBao
- Retries the request once with fresh credential

```go
func TestIntegration_CredentialRotation401(t *testing.T)
```

### 3. Circuit Breaker

Tests that SEAM:
- Tracks consecutive upstream failures
- Opens breaker after threshold
- Returns structured 503 responses
- Resets on successful half-open trial

```go
func TestIntegration_CircuitBreaker(t *testing.T)
```

### 4. Oversized Responses

Tests that SEAM:
- Streams large responses efficiently
- Bounds buffer sizes
- Scrubs streamed content

```go
func TestIntegration_OversizedResponse(t *testing.T)
```

### 5. Timeout Handling

Tests that SEAM:
- Handles upstream timeouts gracefully
- Doesn't leak goroutines

```go
func TestIntegration_Timeout(t *testing.T)
```

### 6. Error Propagation

Tests that SEAM:
- Correctly forwards 5xx errors
- Maintains structured error format

```go
func TestIntegration_5xxError(t *testing.T)
```

## Running Tests

### All Tests (including integration)

```bash
go test -v ./internal/server/...
```

### Only Unit Tests (fast, no external dependencies)

```bash
go test -short ./internal/server/...
```

### Integration Tests Only

```bash
# Requires OpenBao running on localhost:8200
go test -v -run TestIntegration ./internal/server/
```

### With Coverage

```bash
go test -cover ./internal/server/...
```

## CI Considerations

The test rig is designed to work in CI environments:

1. **OpenBao Sidecar**: The CI workflow should start an OpenBao dev container alongside the test container
2. **Network**: Services communicate via `localhost` or Docker networking
3. **Cleanup**: All resources are cleaned up via `defer` and `t.Cleanup()`

Example Argo WorkflowTemplate snippet:

```yaml
containers:
  - name: openbao
    image: hashicorp/vault:1.15
    command: ["server", "-dev", "-dev-listen-address=0.0.0.0:8200"]
    env:
      - name: VAULT_DEV_ROOT_TOKEN_ID
        value: "dev-root-token"
  - name: test
    image: golang:1.26
    command: ["go", "test", "-v", "./internal/server/..."]
    env:
      - name: TEST_OPENBAO_ADDR
        value: "http://openbao:8200"
      - name: TEST_OPENBAO_TOKEN
        value: "dev-root-token"
```

## Design Principles

1. **Deterministic**: Each behavior is scriptable and reproducible
2. **Observable**: Call logs capture all requests for verification
3. **Composable**: Multiple stubs can run concurrently for multi-instance tests
4. **Isolated**: Tests don't share state; each gets fresh instances
5. **Fast**: Unit tests run without external deps; integration tests require only OpenBao

## Future Extensions

Potential enhancements to the test rig:

- **Latency Injection**: Simulate slow upstreams (add `BehaviorSlow`)
- **Retry Testing**: Test idempotency and retry logic
- **TLS Testing**: Stub upstream with self-signed certs
- **Protocol Variations**: gRPC, WebSocket, HTTP/2
- **Fan-out Testing**: Multi-instance map simulation
- **Chaos Testing**: Random failure injection
