package releases

import (
	"fmt"
	"regexp"
	"strconv"
)

// ReleaseIDParts are the fields of a release ID. An application release is
// <service>/<short-source-sha>/<environment>/<workflow-run-number>; a config
// release is <service>/config-<short-gitops-sha>/<environment>.
type ReleaseIDParts struct {
	Kind        Kind
	Service     string
	Environment string
	// ShortSHA is the source commit of an application release, or the
	// GitOps commit of a config release.
	ShortSHA  string
	RunNumber int
}

var applicationReleaseID = regexp.MustCompile(`^([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)/([0-9a-f]{7})/([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)/([1-9][0-9]{0,9})$`)

var configReleaseID = regexp.MustCompile(`^([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)/config-([0-9a-f]{7})/([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)$`)

// ParseReleaseID splits a release ID into its parts.
func ParseReleaseID(id string) (ReleaseIDParts, error) {
	if m := configReleaseID.FindStringSubmatch(id); m != nil {
		return ReleaseIDParts{Kind: KindConfig, Service: m[1], ShortSHA: m[2], Environment: m[3]}, nil
	}
	m := applicationReleaseID.FindStringSubmatch(id)
	if m == nil {
		return ReleaseIDParts{}, fmt.Errorf("release ID %q is not <service>/<short-sha>/<environment>/<run-number> or <service>/config-<short-sha>/<environment>", id)
	}
	run, err := strconv.Atoi(m[4])
	if err != nil {
		return ReleaseIDParts{}, fmt.Errorf("release ID %q: %w", id, err)
	}
	return ReleaseIDParts{Kind: KindApplication, Service: m[1], ShortSHA: m[2], Environment: m[3], RunNumber: run}, nil
}

// ApplicationReleaseID builds the release ID of a workflow run, the same way
// the release workflow does: ${GITHUB_SHA::7}.
func ApplicationReleaseID(service, sourceSHA, environment string, runNumber int) string {
	short := sourceSHA
	if len(short) > 7 {
		short = short[:7]
	}
	return fmt.Sprintf("%s/%s/%s/%d", service, short, environment, runNumber)
}

// ConfigReleaseID is the release ID of a GitOps commit that no CI run made
// (plan section 8.7).
func ConfigReleaseID(service, environment, gitopsSHA string) string {
	short := gitopsSHA
	if len(short) > 7 {
		short = short[:7]
	}
	return fmt.Sprintf("%s/config-%s/%s", service, short, environment)
}
