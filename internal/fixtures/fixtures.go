// Package fixtures inserts sample releases for fixture mode (plan section
// 10.4): frontend work, screenshots and the smoke test in CI.
package fixtures

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/storage"
)

//go:embed data/releases.json
var releasesJSON []byte

// Releases decodes the embedded fixture releases.
func Releases() ([]releases.Release, error) {
	var rs []releases.Release
	if err := json.Unmarshal(releasesJSON, &rs); err != nil {
		return nil, fmt.Errorf("decode fixture releases: %w", err)
	}
	for i := range rs {
		if rs[i].UUID == "" {
			rs[i].UUID = releases.NewUUID()
		}
	}
	return rs, nil
}

// Load inserts the fixture releases that are not already in the store and
// returns how many it inserted. Running it twice is harmless.
func Load(ctx context.Context, store *storage.Store) (int, error) {
	rs, err := Releases()
	if err != nil {
		return 0, err
	}

	inserted := 0
	for _, r := range rs {
		ok, err := store.InsertRelease(ctx, r)
		if err != nil {
			return inserted, err
		}
		if ok {
			inserted++
		}
	}
	return inserted, nil
}
