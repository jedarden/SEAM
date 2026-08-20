package buildinfo

import "testing"

func TestReadPrefersInjectedBuildMetadata(t *testing.T) {
	oldVersion, oldRevision := Version, Revision
	Version, Revision = "1.2.3", "abc123"
	t.Cleanup(func() {
		Version, Revision = oldVersion, oldRevision
	})

	info := Read()
	if info.Version != "1.2.3" {
		t.Errorf("version = %q, want injected version", info.Version)
	}
	if info.Revision != "abc123" {
		t.Errorf("revision = %q, want injected revision", info.Revision)
	}
	if info.GoVersion == "" {
		t.Error("Go version is empty")
	}
	if info.Modified != "true" && info.Modified != "false" {
		t.Errorf("modified = %q, want a Prometheus-safe boolean", info.Modified)
	}
}

func TestNormalizeUsesStableFallbacks(t *testing.T) {
	info := normalize(Info{})
	if info.Version != "dev" || info.Revision != "unknown" || info.GoVersion != "unknown" || info.Modified != "false" {
		t.Fatalf("normalized build info = %+v", info)
	}
}
