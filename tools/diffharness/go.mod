// SEAM differential capture + replay harness.
//
// This is a deliberately standalone module: it depends on the Go standard
// library only, imports no SEAM gateway code, and is built and tested in
// isolation (`go test ./...` from this directory). The plan calls it out as
// "standalone tool; no SEAM code dependency to start — workable now" (Testing
// Strategy → Conformance / differential harness). When the gateway's own
// request/response pipeline exists this package may migrate to consume it; it
// does not today.
module github.com/ardenone/seam/tools/diffharness

go 1.25
