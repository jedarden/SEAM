// Package buildinfo reports the version-control metadata embedded in the SEAM
// binary. Release builds may override Version and Revision with -ldflags; local
// builds fall back to the metadata that the Go tool records automatically.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

var (
	// Version is overridden by release builds with -ldflags.
	Version = "dev"
	// Revision is overridden when the container build is given a source revision.
	Revision string
)

// Info is safe to expose as Prometheus labels. It contains no environment or
// filesystem data.
type Info struct {
	Version   string
	Revision  string
	GoVersion string
	Modified  string
}

// Read returns the best build metadata available for the running binary.
func Read() Info {
	info := Info{
		Version:   Version,
		Revision:  Revision,
		GoVersion: runtime.Version(),
		Modified:  "false",
	}

	goInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return normalize(info)
	}
	if info.Version == "dev" && goInfo.Main.Version != "" && goInfo.Main.Version != "(devel)" {
		info.Version = goInfo.Main.Version
	}
	for _, setting := range goInfo.Settings {
		switch setting.Key {
		case "vcs.revision":
			if info.Revision == "" {
				info.Revision = setting.Value
			}
		case "vcs.modified":
			info.Modified = setting.Value
		}
	}
	return normalize(info)
}

func normalize(info Info) Info {
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Revision == "" {
		info.Revision = "unknown"
	}
	if info.GoVersion == "" {
		info.GoVersion = "unknown"
	}
	if info.Modified != "true" {
		info.Modified = "false"
	}
	return info
}
