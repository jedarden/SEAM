package server

import (
	"os"
	"testing"
	"time"
)

// k8sFragmentPath is the fleet k8s-api-proxy fragment this repo owns the
// source of truth for, relative to this package. The contract test drives the
// real file — not a fixture copy — because the fragment is the only
// x-upstream-map consumer in the fleet and the route-table decode of exactly
// this file is what refused to boot the live gateway (plan.md Phase 10
// CRITICAL FAILURE, verified 2026-09-01). A fixture here would have stayed
// green while production crashlooped.
const k8sFragmentPath = "../../declarative-config/k8s/rs-manager/seam/routes/k8s/k8s-api-proxy.yaml"

// expectedK8sInstances pins the full `_all` envelope membership: every
// credential-free kubectl endpoint in the fleet plus the in-cluster admin
// target. A cluster added to the fragment must land here, or the fan-out
// silently answers without it.
var expectedK8sInstances = map[string]struct {
	url            string
	vaultPath      string
	requiredScopes []string
	probeInterval  time.Duration
}{
	"agent-sandbox":    {"http://traefik-agent-sandbox:8001", "seam/routes/k8s/agent-sandbox-observer-token", []string{"k8s-ro:get"}, time.Hour},
	"ardenone-cluster": {"http://traefik-ardenone-cluster:8001", "seam/routes/k8s/ardenone-cluster-observer-token", []string{"k8s-ro:get"}, time.Hour},
	"ardenone-manager": {"http://traefik-ardenone-manager:8001", "seam/routes/k8s/ardenone-manager-observer-token", []string{"k8s-ro:get"}, time.Hour},
	"apexalgo-iad":     {"http://traefik-apexalgo-iad:8001", "seam/routes/k8s/apexalgo-iad-observer-token", []string{"k8s-ro:get"}, time.Hour},
	"iad-ci":           {"http://traefik-iad-ci:8001", "seam/routes/k8s/iad-ci-observer-token", []string{"k8s-ro:get"}, time.Hour},
	"iad-kalshi":       {"http://kubectl-proxy-iad-kalshi:8001", "seam/routes/k8s/iad-kalshi-observer-token", []string{"k8s-ro:get"}, time.Hour},
	"iad-options":      {"http://traefik-iad-options:8001", "seam/routes/k8s/iad-options-observer-token", []string{"k8s-ro:get"}, time.Hour},
	"ord-devimprint":   {"http://traefik-ord-devimprint:8001", "seam/routes/k8s/ord-devimprint-observer-token", []string{"k8s-ro:get"}, time.Hour},
	"rs-manager":       {"http://traefik-rs-manager:8001", "seam/routes/k8s/rs-manager-observer-token", []string{"k8s-ro:get"}, time.Hour},
	"rs-manager-admin": {"https://kubernetes.default.svc", "seam/routes/k8s/rs-manager-admin-token", []string{"k8s-rw:get"}, 45 * time.Minute},
}

// k8sFragmentRoute finds the built entry for one of the fragment's
// parameterized paths.
func k8sFragmentRoute(t *testing.T, table *RouteTable, pathTemplate string) RouteEntry {
	t.Helper()
	for _, entry := range table.GetRoutes() {
		if entry.PathTemplate == pathTemplate && entry.Method == "GET" {
			return entry
		}
	}
	t.Fatalf("route GET %s not found in built table", pathTemplate)
	return RouteEntry{}
}

// TestRouteTable_K8sFleetFragmentContract pins the fleet fragment end to end
// through the documented pipeline (fragment load, merge, route-table build):
// the `_all` envelope's cluster list accounts for every upstream, each
// per-cluster passthrough target resolves to its own endpoint, and the
// per-instance authorization keys survive the decode in their documented
// shapes.
func TestRouteTable_K8sFleetFragmentContract(t *testing.T) {
	contents, err := os.ReadFile(k8sFragmentPath)
	if err != nil {
		t.Fatalf("read fleet k8s fragment: %v", err)
	}
	table := requiredScopeTableFromFragments(t, map[string]string{"k8s": string(contents)})

	entry := k8sFragmentRoute(t, table, "/k8s/{cluster}/api/v1/namespaces")

	if entry.InstanceParam != "cluster" {
		t.Errorf("x-instance-param = %q, want cluster", entry.InstanceParam)
	}

	if got := len(entry.UpstreamMap); got != len(expectedK8sInstances) {
		t.Errorf("upstream map has %d instances, want %d (got: %v)", got, len(expectedK8sInstances), instanceNames(entry.UpstreamMap))
	}

	for instanceID, want := range expectedK8sInstances {
		target, ok := entry.UpstreamMap[instanceID]
		if !ok {
			t.Errorf("upstream map is missing instance %q — the _all envelope would answer without it", instanceID)
			continue
		}
		if target.URL != want.url {
			t.Errorf("instance %q url = %q, want %q", instanceID, target.URL, want.url)
		}
		if target.VaultPath != want.vaultPath {
			t.Errorf("instance %q vaultPath = %q, want %q", instanceID, target.VaultPath, want.vaultPath)
		}
		if len(target.RequiredScopes) != len(want.requiredScopes) || (len(want.requiredScopes) > 0 && target.RequiredScopes[0] != want.requiredScopes[0]) {
			t.Errorf("instance %q requiredScope = %v, want %v", instanceID, target.RequiredScopes, want.requiredScopes)
		}
		if target.ProbeInterval != want.probeInterval {
			t.Errorf("instance %q probeInterval = %v, want %v", instanceID, target.ProbeInterval, want.probeInterval)
		}
		if target.InjectAs == nil || target.InjectAs.Kind != InjectionBearer {
			t.Errorf("instance %q injectAs = %v, want bearer injection", instanceID, target.InjectAs)
		}
	}

	if scopes := entry.FanoutScope["_default"]; len(scopes) != 1 || scopes[0] != "k8s-ro:fanout" {
		t.Errorf("x-fanout-scope default = %v, want [k8s-ro:fanout]", scopes)
	}
}

// instanceNames is a diagnostic helper for the membership failure message.
func instanceNames(upstreamMap map[string]RouteTarget) []string {
	names := make([]string, 0, len(upstreamMap))
	for name := range upstreamMap {
		names = append(names, name)
	}
	return names
}

// TestRouteTable_UpstreamMapEntryShapes pins the documented entry shapes at
// both ends: requiredScope as bare-string sugar and as an array, injectAs as
// the documented inline object and as a JSON-encoded string, and an entry
// carrying only the required url.
func TestRouteTable_UpstreamMapEntryShapes(t *testing.T) {
	fragment := `x-seam-schema: v1
x-seam-owner: map-shapes
x-api-version: v1
x-instance-param: cluster
x-fanout-scope: map-shapes:fanout
x-required-scope: [map-shapes:read]
paths:
  /items/{cluster}:
    get:
      x-upstream-map:
        scalar-scope:
          url: https://alpha.internal
          vaultPath: rs-manager/rs-manager/seam/routes/map-shapes/alpha
          injectAs: {kind: bearer}
          requiredScope: map-shapes:alpha
        list-scope:
          url: https://beta.internal
          vaultPath: rs-manager/rs-manager/seam/routes/map-shapes/beta
          injectAs: '{"kind": "header", "name": "X-Key"}'
          requiredScope: [map-shapes:beta]
        bare:
          url: https://gamma.internal
      responses:
        '200':
          description: ok
`
	table := requiredScopeTableFromFragments(t, map[string]string{"map-shapes": fragment})
	entry := k8sFragmentRoute(t, table, "/items/{cluster}")

	scalar := entry.UpstreamMap["scalar-scope"]
	if len(scalar.RequiredScopes) != 1 || scalar.RequiredScopes[0] != "map-shapes:alpha" {
		t.Errorf("scalar requiredScope = %v, want bare-string sugar for [map-shapes:alpha]", scalar.RequiredScopes)
	}
	if scalar.InjectAs == nil || scalar.InjectAs.Kind != InjectionBearer {
		t.Errorf("inline-object injectAs = %v, want bearer injection", scalar.InjectAs)
	}

	listed := entry.UpstreamMap["list-scope"]
	if len(listed.RequiredScopes) != 1 || listed.RequiredScopes[0] != "map-shapes:beta" {
		t.Errorf("array requiredScope = %v, want [map-shapes:beta]", listed.RequiredScopes)
	}
	if listed.InjectAs == nil || listed.InjectAs.Kind != InjectionHeader || listed.InjectAs.Name != "X-Key" {
		t.Errorf("json-string injectAs = %+v, want {header X-Key}", listed.InjectAs)
	}

	bare := entry.UpstreamMap["bare"]
	if bare.URL != "https://gamma.internal" {
		t.Errorf("bare entry url = %q, want https://gamma.internal", bare.URL)
	}
	if len(bare.RequiredScopes) != 0 {
		t.Errorf("bare entry requiredScope = %v, want none", bare.RequiredScopes)
	}
	if bare.InjectAs != nil {
		t.Errorf("bare entry injectAs = %v, want none", bare.InjectAs)
	}

	if scopes := entry.FanoutScope["_default"]; len(scopes) != 1 || scopes[0] != "map-shapes:fanout" {
		t.Errorf("scalar x-fanout-scope = %v, want [_default: [map-shapes:fanout]]", scopes)
	}
}
