// Package testutil_examples provides examples of how to use the SEAM test rig
// for integration testing. These are NOT actual tests - they're documentation
// in executable form.
package testutil_examples

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ardenone/seam/internal/testutil/openbao"
	"github.com/ardenone/seam/internal/testutil/stubupstream"
)

// Example_stubUpstream demonstrates basic stub upstream usage.
func Example_stubUpstream() {
	// Start a stub upstream server in echo mode
	stub := stubupstream.New(stubupstream.Config{
		Addr:     "localhost:15820",
		Behavior: stubupstream.BehaviorEcho,
	})

	if err := stub.Start(); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = stub.Stop(context.Background()) }()

	// Make a request (in real code, use http.Get)
	// The stub will echo back any Authorization header in an error body

	// Inspect the call log
	calls := stub.GetCallLog()
	for _, call := range calls {
		fmt.Printf("Request to %s with auth present: %t\n", call.Path, call.AuthHeader != "")
	}
}

// Example_stubUpstream_behaviorChange shows how to change stub behavior dynamically.
func Example_stubUpstream_behaviorChange() {
	stub := stubupstream.New(stubupstream.Config{
		Addr:     "localhost:15821",
		Behavior: stubupstream.BehaviorNormal,
	})

	_ = stub.Start()
	defer func() { _ = stub.Stop(context.Background()) }()

	// Initially returns 200 OK

	// Change to return 401 Unauthorized (for testing credential rotation)
	stub.SetBehavior(stubupstream.Behavior401)

	// Now returns 401
}

// Example_stubUpstream_circuitBreaker simulates circuit breaker testing.
func Example_stubUpstream_circuitBreaker() {
	stub := stubupstream.New(stubupstream.Config{
		Addr:          "localhost:15822",
		Behavior:      stubupstream.BehaviorTransportFault,
		FailThreshold: 5,
	})

	_ = stub.Start()
	defer func() { _ = stub.Stop(context.Background()) }()

	// The stub will return transport faults
	// After 5 consecutive failures, a circuit breaker should open
	// Further requests should fail-fast with 503 without hitting the upstream
}

// Example_openbao_managed shows the easiest way to use OpenBao in tests.
func Example_openbao_managed() {
	// In a test function:
	// server := openbao.ManageTestServer(t)
	//
	// The test helper:
	// - Starts an OpenBao dev server
	// - Sets up test secrets
	// - Automatically cleans up when the test completes
	//
	// client := server.Client()
	// ctx := context.Background()
	// secret, _ := client.ReadSecret(ctx, "seam/routes/test/token")
	// _ = secret
}

// Example_openbao_manual shows manual OpenBao server management.
func Example_openbao_manual() {
	cfg := openbao.ServerConfig{
		DevToken:   "my-test-token",
		ListenAddr: "localhost:18200",
	}

	server, err := openbao.NewServer(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = server.Close() }()

	// Set up test secrets
	ctx := context.Background()
	_ = server.SetupTestSecrets(ctx)

	// Use the server's client
	client := server.Client()
	_, _ = client.ReadSecret(ctx, "seam/routes/testservice/token")
	fmt.Println("Secret retrieved")
}

// Example_openbao_rotation demonstrates credential rotation testing.
func Example_openbao_rotation() {
	server, _ := openbao.NewServer(openbao.ServerConfig{
		ListenAddr: "localhost:18201",
	})
	defer func() { _ = server.Close() }()

	ctx := context.Background()
	_ = server.SetupTestSecrets(ctx)

	// Rotate a credential
	newSecret, err := server.RotateCredential(ctx, "seam/routes/testservice/token", "token")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Credential rotated: %t\n", newSecret != "")

	// Now test that SEAM handles the 401 from the old credential
	// and self-heals by refetching the new one
}

// Example_openbao_existing shows connecting to an existing OpenBao instance.
func Example_openbao_existing() {
	// If OpenBao is already running (e.g., in CI):
	// client, err := openbao.NewClientForTesting()
	// if err != nil {
	//     // OpenBao not available - skip the test
	//     t.Skipf("OpenBao not available: %v", err)
	// }
	//
	// // Or use the helper:
	// openbao.SkipIfNoOpenBao(t)
	//
	// ctx := context.Background()
	// client.WriteSecret(ctx, "seam/routes/test/token", map[string]interface{}{
	//     "token": "test-secret",
	// })
}

// Example_integration_credentialScrubbing demonstrates a full integration test.
func Example_integration_credentialScrubbing() {
	// This example shows testing the complete credential injection and scrubbing flow
	// In a real test, this would be a test function

	// 1. Start stub upstream that echoes credentials
	stub := stubupstream.New(stubupstream.Config{
		Addr:     "localhost:15830",
		Behavior: stubupstream.BehaviorEcho,
	})
	_ = stub.Start()
	defer func() { _ = stub.Stop(context.Background()) }()

	// 2. Set up OpenBao with test secret
	server, _ := openbao.NewServer(openbao.ServerConfig{
		ListenAddr: "localhost:18202",
	})
	defer func() { _ = server.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = server.SetupTestSecrets(ctx)
	obClient := server.Client()

	// 3. Test the flow:
	//    - SEAM fetches secret from OpenBao
	//    - SEAM injects into upstream request
	//    - Stub echoes the credential
	//    - SEAM scrubs the response
	//    - Caller gets redacted response

	secret, _ := obClient.ReadSecret(ctx, "seam/routes/testservice/token")
	token := secret["token"]

	// In real code: make HTTP request through SEAM
	// Verify response contains [REDACTED-BY-SEAM] instead of token
	_ = token
}

// Example_integration_credentialRotation tests the self-heal flow.
func Example_integration_credentialRotation() {
	// 1. Start stub that returns 401
	stub := stubupstream.New(stubupstream.Config{
		Addr:     "localhost:15831",
		Behavior: stubupstream.Behavior401,
	})
	_ = stub.Start()
	defer func() { _ = stub.Stop(context.Background()) }()

	// 2. Start OpenBao with initial secret
	server, _ := openbao.NewServer(openbao.ServerConfig{
		ListenAddr: "localhost:18203",
	})
	defer func() { _ = server.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	obClient := server.Client()
	_ = obClient.WriteSecret(ctx, "seam/routes/rotating/token", map[string]interface{}{
		"token": "initial-credential",
	})

	// 3. Test the self-heal flow:
	//    - First request with old credential gets 401
	//    - Rotate credential in OpenBao
	//    - SEAM invalidates cache
	//    - SEAM refetches new credential
	//    - SEAM retries request
	//    - Request succeeds (or gets proper error if still failing)

	// Rotate the credential
	newCred, _ := server.RotateCredential(ctx, "seam/routes/rotating/token", "token")
	_ = newCred
}

// Example_integration_circuitBreaker demonstrates circuit breaker testing.
func Example_integration_circuitBreaker() {
	// 1. Start stub that returns transport faults
	stub := stubupstream.New(stubupstream.Config{
		Addr:          "localhost:15832",
		Behavior:      stubupstream.BehaviorTransportFault,
		FailThreshold: 5,
	})
	_ = stub.Start()
	defer func() { _ = stub.Stop(context.Background()) }()

	// 2. Make multiple requests
	//    - First 5 requests should get transport errors
	//    - 6th request should get 503 (breaker open)
	//    - Requests should NOT hit upstream while breaker is open
	//    - After 30 seconds, one trial request should be allowed (half-open)
	//    - If successful, breaker closes

	// In real code, track breaker state and verify behavior
}

// Example_stubUpstream_allBehaviors lists all available behaviors.
func Example_stubUpstream_allBehaviors() {
	behaviors := []struct {
		name string
		mode stubupstream.Behavior
		desc string
	}{
		{"Echo", stubupstream.BehaviorEcho, "Echoes credential in error body"},
		{"401", stubupstream.Behavior401, "Returns 401 Unauthorized"},
		{"5xx", stubupstream.Behavior5xx, "Returns 500 Internal Server Error"},
		{"Timeout", stubupstream.BehaviorTimeout, "Hangs until timeout"},
		{"Upgrade", stubupstream.BehaviorUpgrade, "Signals protocol upgrade"},
		{"Oversized", stubupstream.BehaviorOversized, "Returns oversized response"},
		{"TransportFault", stubupstream.BehaviorTransportFault, "Simulates transport failure"},
		{"Normal", stubupstream.BehaviorNormal, "Returns normal 200 OK"},
	}

	for _, b := range behaviors {
		fmt.Printf("%s: %s\n", b.name, b.desc)
	}
}
