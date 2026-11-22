// Package mybuilds holds the service's semantic version, read from the VERSION file beside this one.
//
// The version names what mybuilds does for its callers: the senders of signed events (GitHub, Argo CD and the
// CI workflows that post to /api/v1/ci-events), and anyone reading /api/v1/*. Bump MAJOR when a caller that
// worked before would now fail, MINOR for a new feature or a changed default that keeps callers working, PATCH
// for a fix. Changes to CI, docs or tests alone need no bump.
//
// The file is embedded, so `go run`, the tests and the image all report the same version without build flags.
// A malformed file panics at startup, which fails the release's smoke test before anything is published.
package mybuilds

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
)

//go:embed VERSION
var versionFile string

// semver is MAJOR.MINOR.PATCH without leading zeros, with an optional pre-release (1.12.0-rc.1) or build
// (+sha.0123456).
var semver = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// Version is the semantic version in VERSION.
var Version = mustParseVersion(versionFile)

// ParseVersion returns the version in text, the contents of a VERSION file, or an error if it is not one.
func ParseVersion(text string) (string, error) {
	version := strings.TrimSpace(text)
	if !semver.MatchString(version) {
		return "", fmt.Errorf("VERSION must hold a semantic version such as 1.11.0, not %q", version)
	}
	return version, nil
}

func mustParseVersion(text string) string {
	version, err := ParseVersion(text)
	if err != nil {
		panic(err)
	}
	return version
}
