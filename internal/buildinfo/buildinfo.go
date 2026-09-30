// Package buildinfo describes the running binary: the release version, the commit and date it was built
// from (set by the Dockerfile through -ldflags, because .git is not in the build context) and what the Go
// toolchain recorded about itself. Nothing here contacts anything.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Info is what -ldflags set in package main.
type Info struct {
	Version   string // "dev" for a build without a version
	Commit    string // full or short commit hash; "" when unknown
	BuildDate string // RFC 3339 UTC; "" when unknown
}

// Unknown is what a field reports when it was not set at build time.
const Unknown = "unknown"

// orUnknown maps an unset build value to Unknown. The Dockerfile passes the build args straight through,
// and an unset ARG reaches the flag as "" or as its "unknown" default, so both mean not set.
func orUnknown(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return Unknown
	}
	return s
}

// Normalized returns the info with unset fields as Unknown (Version stays "dev").
func (i Info) Normalized() Info {
	if strings.TrimSpace(i.Version) == "" {
		i.Version = "dev"
	}
	i.Commit = orUnknown(i.Commit)
	i.BuildDate = orUnknown(i.BuildDate)
	return i
}

// GoVersion is the toolchain that built the binary.
func GoVersion() string { return runtime.Version() }

// OSArch is GOOS/GOARCH of the binary.
func OSArch() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Modules returns "path version" for each module the binary was built from, main module excluded. Empty
// when the binary carries no build info (some test builds).
func Modules() []string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	out := make([]string, 0, len(bi.Deps))
	for _, d := range bi.Deps {
		if d.Replace != nil {
			d = d.Replace
		}
		out = append(out, d.Path+" "+d.Version)
	}
	return out
}

// Report is the text of `kipple version -v`. The first line is the same as plain `kipple version` (the
// release procedure reads it), the rest are "key: value" lines. schema and web are the embedded schema
// version and web build id, which this package cannot know.
func (i Info) Report(schema int, web string) string {
	i = i.Normalized()
	var b strings.Builder
	fmt.Fprintln(&b, i.Version)
	fmt.Fprintf(&b, "commit: %s\n", i.Commit)
	fmt.Fprintf(&b, "built: %s\n", i.BuildDate)
	fmt.Fprintf(&b, "go: %s\n", GoVersion())
	fmt.Fprintf(&b, "platform: %s\n", OSArch())
	fmt.Fprintf(&b, "schema: %d\n", schema)
	fmt.Fprintf(&b, "web build: %s\n", orUnknown(web))
	if mods := Modules(); len(mods) > 0 {
		fmt.Fprintf(&b, "modules: %d\n", len(mods))
		for _, m := range mods {
			fmt.Fprintf(&b, "  %s\n", m)
		}
	}
	return b.String()
}
