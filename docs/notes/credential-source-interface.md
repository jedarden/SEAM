# Credential source interface

Phase 15 places every runtime credential read behind the server's
`CredentialSource` interface:

```go
type CredentialSource interface {
    Resolve(context.Context, RouteEntry) ([]byte, error)
    Refreshable(context.Context, RouteEntry) ([]byte, error)
}
```

`Resolve` is used for ordinary request injection and sentinel probes.
`Refreshable` is the forced-read path after an upstream 401; a source must
invalidate any cached value before returning the replacement. The existing
OpenBao client is adapted by the vault source, which also owns the existing
OAuth refresh-token exchange and cache. No route-fragment fields or wire
behavior change in this phase.

The source is injectable in tests through `RouteTable.SetCredentialSource` and
`ProbeLoopConfig.CredentialSource`. The request context retains a compatibility
adapter for the existing function-based proxy tests while production wiring
uses the interface directly.
