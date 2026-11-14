package storage

import (
	"context"
	"testing"
	"time"

	"github.com/spicyhek/mybuilds/internal/releases"
)

// seedHistory stores seven releases. Two share a creation time, so the
// cursor has to break the tie by release ID.
func seedHistory(t *testing.T, store *Store) {
	t.Helper()
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	type spec struct {
		id         string
		service    string
		kind       releases.Kind
		status     releases.State
		drill      bool
		superseded bool
		minute     int
	}
	for _, s := range []spec{
		{"personal-site/aaaaaaa/production/1", "personal-site", releases.KindApplication, releases.StateSucceeded, false, false, 0},
		{"personal-site/bbbbbbb/production/2", "personal-site", releases.KindApplication, releases.StateFailed, false, false, 10},
		{"personal-site/ccccccc/production/3", "personal-site", releases.KindApplication, releases.StateSucceeded, false, true, 20},
		{"personal-site/config-ddddddd/production", "personal-site", releases.KindConfig, releases.StateSucceeded, false, false, 30},
		{"drill-target/eeeeeee/production/1", "drill-target", releases.KindApplication, releases.StateFailed, true, false, 40},
		{"drill-target/fffffff/production/2", "drill-target", releases.KindApplication, releases.StateSucceeded, true, false, 50},
		{"drill-target/ggggggg/production/3", "drill-target", releases.KindApplication, releases.StateDegraded, true, false, 50},
	} {
		tmpl := releases.TemplateApplication
		if s.kind == releases.KindConfig {
			tmpl = releases.TemplateConfig
		}
		r := releases.Release{
			UUID:        releases.NewUUID(),
			ReleaseID:   s.id,
			Service:     s.service,
			Environment: "production",
			Kind:        s.kind,
			Template:    tmpl,
			Drill:       s.drill,
			Superseded:  s.superseded,
			Status:      s.status,
			CreatedAt:   base.Add(time.Duration(s.minute) * time.Minute),
			Stages:      []releases.Stage{{Key: "source", State: s.status}},
		}
		if _, err := store.InsertRelease(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
}

func ids(list []releases.Release) []string {
	out := make([]string, len(list))
	for i, r := range list {
		out[i] = r.ReleaseID
	}
	return out
}

func TestListReleasesPageWalksEveryRelease(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	seedHistory(t, store)

	var got []string
	f := ReleaseFilter{Limit: 2}
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination does not end")
		}
		list, next, err := store.ListReleasesPage(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ids(list)...)
		if next == nil {
			break
		}
		f.After = next
	}
	want := []string{
		"drill-target/fffffff/production/2",
		"drill-target/ggggggg/production/3",
		"drill-target/eeeeeee/production/1",
		"personal-site/config-ddddddd/production",
		"personal-site/ccccccc/production/3",
		"personal-site/bbbbbbb/production/2",
		"personal-site/aaaaaaa/production/1",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestListReleasesPageLastPageHasNoCursor(t *testing.T) {
	store := openTestStore(t)
	seedHistory(t, store)
	list, next, err := store.ListReleasesPage(context.Background(), ReleaseFilter{Limit: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 7 || next != nil {
		t.Fatalf("got %d releases, next %v; want 7 and no cursor", len(list), next)
	}
	if len(list[0].Stages) != 1 {
		t.Errorf("stages not loaded: %+v", list[0].Stages)
	}
}

func TestListReleasesPageFilters(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	seedHistory(t, store)
	yes, no := true, false

	for _, tc := range []struct {
		name string
		f    ReleaseFilter
		want []string
	}{
		{"service", ReleaseFilter{Service: "drill-target"}, []string{
			"drill-target/fffffff/production/2", "drill-target/ggggggg/production/3", "drill-target/eeeeeee/production/1"}},
		{"kind", ReleaseFilter{Kind: "config"}, []string{"personal-site/config-ddddddd/production"}},
		{"failed", ReleaseFilter{Outcome: "failed"}, []string{
			"drill-target/eeeeeee/production/1", "personal-site/bbbbbbb/production/2"}},
		{"succeeded leaves out superseded", ReleaseFilter{Outcome: "succeeded", Service: "personal-site"}, []string{
			"personal-site/config-ddddddd/production", "personal-site/aaaaaaa/production/1"}},
		{"skipped means superseded", ReleaseFilter{Outcome: "skipped"}, []string{"personal-site/ccccccc/production/3"}},
		{"only drills", ReleaseFilter{Drill: &yes, Outcome: "degraded"}, []string{"drill-target/ggggggg/production/3"}},
		{"no drills", ReleaseFilter{Drill: &no, Kind: "application"}, []string{
			"personal-site/ccccccc/production/3", "personal-site/bbbbbbb/production/2", "personal-site/aaaaaaa/production/1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.f.Limit = 50
			list, next, err := store.ListReleasesPage(ctx, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			got := ids(list)
			if next != nil || len(got) != len(tc.want) {
				t.Fatalf("got %v (next %v), want %v", got, next, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
