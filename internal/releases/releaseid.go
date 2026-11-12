package releases

import (
	"fmt"
	"regexp"
	"strconv"
)

// ReleaseIDParts are the fields of an application release ID,
// <service>/<short-source-sha>/<environment>/<workflow-run-number>.
type ReleaseIDParts struct {
	Service     string
	ShortSHA    string
	Environment string
	RunNumber   int
}

var applicationReleaseID = regexp.MustCompile(`^([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)/([0-9a-f]{7})/([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)/([1-9][0-9]{0,9})$`)

// ParseReleaseID splits an application release ID into its parts.
func ParseReleaseID(id string) (ReleaseIDParts, error) {
	m := applicationReleaseID.FindStringSubmatch(id)
	if m == nil {
		return ReleaseIDParts{}, fmt.Errorf("release ID %q is not <service>/<short-sha>/<environment>/<run-number>", id)
	}
	run, err := strconv.Atoi(m[4])
	if err != nil {
		return ReleaseIDParts{}, fmt.Errorf("release ID %q: %w", id, err)
	}
	return ReleaseIDParts{Service: m[1], ShortSHA: m[2], Environment: m[3], RunNumber: run}, nil
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
