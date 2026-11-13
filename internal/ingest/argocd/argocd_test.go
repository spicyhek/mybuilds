package argocd

import (
	"encoding/json"
	"testing"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/services"
)

const revision = "89abcdef0123456789abcdef0123456789abcdef"

func TestConfigReleaseKeys(t *testing.T) {
	svcs, err := services.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, trigger, app, message, want string
	}{
		{"human commit", events.TriggerSyncSucceeded, "personal-site",
			"Raise personal-site memory limit\n\nIt ran close to the limit.", "personal-site/config-89abcde/production"},
		{"running sync", events.TriggerSyncRunning, "personal-site", "Raise personal-site memory limit",
			"personal-site/config-89abcde/production"},
		{"CI commit", events.TriggerSyncSucceeded, "personal-site",
			"Deploy personal-site sha-0123456 (#70)\n\nRelease-Id: personal-site/0123456/production/13", ""},
		{"no commit metadata", events.TriggerSyncSucceeded, "personal-site", "", ""},
		{"health notification", events.TriggerHealthDegraded, "personal-site", "Raise personal-site memory limit", ""},
		{"root application", events.TriggerSyncSucceeded, "root", "Raise personal-site memory limit", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{
				"trigger": tc.trigger, "application": tc.app, "revision": revision,
				"started_at": "2026-11-13T18:00:00Z", "finished_at": "2026-11-13T18:00:04Z",
				"commit_message": tc.message, "commit_author": "spicyhek",
			})
			ev, err := Translate(body, "hash", svcs)
			if err != nil {
				t.Fatal(err)
			}
			if ev.Keys.ReleaseID != tc.want || ev.Keys.Revision != revision {
				t.Errorf("keys %+v, want release %q", ev.Keys, tc.want)
			}
		})
	}
}
