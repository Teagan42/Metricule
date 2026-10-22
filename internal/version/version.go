// Package version exposes build metadata.
//
// Values are injected with -ldflags at release time and feed both the version
// command and the metricule_build_info family (§13).
package version

import "runtime"

var (
	// Version is the release version, such as "v0.1.0".
	Version = "dev"
	// Commit is the source revision.
	Commit = "unknown"
	// Date is the build timestamp in RFC 3339 form.
	Date = "unknown"
)

// Info describes this build.
type Info struct {
	Version   string
	Commit    string
	Date      string
	GoVersion string
	Platform  string
}

// Get returns the current build metadata.
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}
