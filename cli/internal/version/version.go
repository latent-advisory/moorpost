// Package version exposes the CLI's version metadata, populated at build
// time via `-ldflags -X`. Default values are placeholders; callers should
// inspect Info() rather than the bare vars.
//
// Build-time injection example (see Makefile):
//
//	go build -ldflags "\
//	  -X github.com/.../version.Version=v0.1.0 \
//	  -X github.com/.../version.Commit=abc1234 \
//	  -X github.com/.../version.Date=2026-05-05T03:55:14Z"
package version

import "fmt"

// Version is the semver-style version string. Overridden at build time.
var Version = "dev"

// Commit is the short git commit SHA. Overridden at build time.
var Commit = "unknown"

// Date is the UTC build timestamp. Overridden at build time.
var Date = "unknown"

// Info returns a one-line human-readable version summary suitable for
// `moorpost --version` output.
func Info() string {
	if Version == "dev" {
		return fmt.Sprintf("dev (commit %s, built %s)", Commit, Date)
	}
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
