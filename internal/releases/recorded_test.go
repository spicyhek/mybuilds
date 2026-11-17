package releases_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
)

var update = flag.Bool("update", false, "rewrite the .golden.json snapshots of recorded releases")

// snapshot is what a recorded release must keep projecting to (plan
// section 17, Replay). The release is stored in full; the timeline as one
// readable line per frame, plus a hash of the full frames so any change to
// a frame's steps or summaries is caught too.
type snapshot struct {
	ReleaseID    string           `json:"release_id"`
	Events       int              `json:"events"`
	Frames       []string         `json:"frames"`
	FramesSHA256 string           `json:"frames_sha256"`
	Release      releases.Release `json:"release"`
}

// TestRecordedReleases replays every recorded release in
// testdata/releases (plan section 7.5): loaded into an empty store, its
// events must project to the stored snapshot, replay twice to the same
// timeline, and end the timeline at the stored release.
func TestRecordedReleases(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "releases", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("no recorded releases yet; see testdata/releases/README.md")
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".jsonl")
		t.Run(name, func(t *testing.T) {
			got := replayRecording(t, file)
			golden := strings.TrimSuffix(file, ".jsonl") + ".golden.json"
			b, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			b = append(b, '\n')
			if *update {
				if err := os.WriteFile(golden, b, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v; write it with: go test ./internal/releases -run TestRecordedReleases -update", err)
			}
			want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
			if !bytes.Equal(b, want) {
				t.Errorf("%s no longer projects to %s:\n%s\nIf the change is intended, rerun with -update and review the diff.",
					file, filepath.Base(golden), firstDifference(string(want), string(b)))
			}
		})
	}
}

func replayRecording(t *testing.T, file string) snapshot {
	t.Helper()
	ctx := context.Background()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := events.ReadRecording(f)
	f.Close()
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}

	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Events go in ID order, so ties in provider time break the same way.
	for _, r := range rec.Events {
		e := r.Event()
		e.ID = 0
		if _, err := store.InsertEvent(ctx, &e); err != nil {
			t.Fatal(err)
		}
	}
	svcs, err := services.Load("")
	if err != nil {
		t.Fatal(err)
	}
	p := releases.NewProjector(store, svcs, nil)
	if _, err := p.ProjectRelease(ctx, rec.ReleaseID); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetRelease(ctx, rec.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	stored.UUID = ""

	frames, err := p.Timeline(ctx, rec.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := p.Timeline(ctx, rec.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	if framesJSON(t, frames) != framesJSON(t, again) {
		t.Error("replaying twice gave different timelines")
	}
	if len(frames) == 0 {
		t.Fatal("no frames")
	}
	for i := 1; i < len(frames); i++ {
		if frames[i].At.Before(frames[i-1].At) {
			t.Errorf("frame %d goes back in time", i)
		}
	}
	last := frames[len(frames)-1].Release
	last.UUID = ""
	if a, b := mustJSON(t, last), mustJSON(t, *stored); a != b {
		t.Errorf("last frame differs from the stored release:\nframe   %s\nrelease %s", a, b)
	}

	// The fixture is self-contained: exporting it again gives back every
	// event it holds.
	out, err := p.RecordedEvents(ctx, rec.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(rec.Events) {
		t.Errorf("re-export has %d events, the recording %d", len(out), len(rec.Events))
	}

	sum := sha256.Sum256([]byte(framesJSON(t, frames)))
	s := snapshot{ReleaseID: rec.ReleaseID, Events: len(rec.Events), FramesSHA256: hex.EncodeToString(sum[:]), Release: *stored}
	for _, f := range frames {
		s.Frames = append(s.Frames, frameLine(f))
	}
	return s
}

// frameLine is one frame as a line: when, which event, the release status
// and each stage's state with its step count.
func frameLine(f releases.Frame) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s |", f.At.UTC().Format(time.RFC3339), f.EventType, f.Release.Status)
	for _, st := range f.Release.Stages {
		fmt.Fprintf(&b, " %s=%s", st.Key, st.State)
		if n := len(st.Steps); n > 0 {
			fmt.Fprintf(&b, "(%d)", n)
		}
	}
	return b.String()
}

func framesJSON(t *testing.T, frames []releases.Frame) string {
	t.Helper()
	type frame struct {
		At        time.Time        `json:"at"`
		EventType string           `json:"event_type"`
		Release   releases.Release `json:"release"`
	}
	out := make([]frame, 0, len(frames))
	for _, f := range frames {
		r := f.Release
		r.UUID = ""
		out = append(out, frame{At: f.At, EventType: f.EventType, Release: r})
	}
	return mustJSON(t, out)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// firstDifference shows the first line where two snapshots differ.
func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d\nwant %s\n got %s", i+1, wl, gl)
		}
	}
	return "(no line differs)"
}
