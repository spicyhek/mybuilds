// Command record-release saves a release's events from a running mybuilds
// as a test fixture (plan section 7.5, docs/replay.md):
//
//	go run ./cmd/record-release -o internal/releases/testdata/releases/personal-site-14.jsonl \
//	    personal-site/472d44a/production/14
//
// Then write its expected projection and timeline with
//
//	go test ./internal/releases -run TestRecordedReleases -update
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
)

func main() {
	from := flag.String("from", "https://mybuilds.brendanmanley.com", "base URL of the mybuilds instance")
	out := flag.String("o", "", "fixture file to write (required)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: record-release [-from URL] -o FILE RELEASE_ID")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *out == "" || flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := record(*from, flag.Arg(0), *out); err != nil {
		fmt.Fprintln(os.Stderr, "record-release:", err)
		os.Exit(1)
	}
}

func record(from, releaseID, out string) error {
	base, err := url.Parse(strings.TrimSuffix(from, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("-from %q is not a URL", from)
	}
	// The release ID's slashes are path separators the server expects.
	endpoint := base.JoinPath("api", "v1", "releases").String() + "/" + releaseID + "/events"

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", endpoint, resp.Status)
	}
	var body struct {
		ReleaseID string          `json:"release_id"`
		Events    []events.Record `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	if body.ReleaseID != releaseID {
		return fmt.Errorf("asked for %s, got %s", releaseID, body.ReleaseID)
	}
	if len(body.Events) == 0 {
		return fmt.Errorf("%s has no events", releaseID)
	}

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	rec := events.Recording{
		ReleaseID:    releaseID,
		RecordedFrom: base.Host,
		RecordedAt:   time.Now().UTC().Truncate(time.Second),
		Events:       body.Events,
	}
	if err := events.WriteRecording(f, rec); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("%s: %d events of %s\n", out, len(body.Events), releaseID)
	return nil
}
