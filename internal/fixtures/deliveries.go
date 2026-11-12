package fixtures

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/ingest"
	"github.com/spicyhek/mybuilds/internal/ingest/verify"
)

//go:embed deliveries/*/*.json
var deliveryFiles embed.FS

// RecordedRelease is the release the recorded deliveries describe.
const RecordedRelease = "personal-site/ce61154/production/11"

// Delivery is one recorded provider request: the body as the provider sent
// it and the headers mybuilds reads. Signatures are not recorded; callers
// sign the body with their own secret.
type Delivery struct {
	Name     string
	Provider string            `json:"provider"`
	Headers  map[string]string `json:"headers"`
	Body     json.RawMessage   `json:"body"`
}

// OccurredAt is the provider timestamp the delivery carries, used to space
// out a replay the way the real release was spaced.
func (d Delivery) OccurredAt() time.Time {
	var probe struct {
		OccurredAt  time.Time `json:"occurred_at"`
		FinishedAt  string    `json:"finished_at"`
		StartedAt   string    `json:"started_at"`
		WorkflowRun *struct {
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"workflow_run"`
		WorkflowJob *struct {
			Status      string     `json:"status"`
			StartedAt   *time.Time `json:"started_at"`
			CompletedAt *time.Time `json:"completed_at"`
		} `json:"workflow_job"`
	}
	_ = json.Unmarshal(d.Body, &probe)
	switch {
	case !probe.OccurredAt.IsZero():
		return probe.OccurredAt
	case probe.WorkflowRun != nil:
		return probe.WorkflowRun.UpdatedAt
	case probe.WorkflowJob != nil && probe.WorkflowJob.Status == "completed" && probe.WorkflowJob.CompletedAt != nil:
		return *probe.WorkflowJob.CompletedAt
	case probe.WorkflowJob != nil && probe.WorkflowJob.StartedAt != nil:
		return *probe.WorkflowJob.StartedAt
	}
	for _, s := range []string{probe.FinishedAt, probe.StartedAt} {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Deliveries returns the recorded deliveries of one release in live order.
func Deliveries(name string) ([]Delivery, error) {
	dir := path.Join("deliveries", name)
	entries, err := fs.ReadDir(deliveryFiles, dir)
	if err != nil {
		return nil, fmt.Errorf("recorded deliveries %q: %w", name, err)
	}
	var out []Delivery
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := deliveryFiles.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var d Delivery
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		d.Name = strings.TrimSuffix(e.Name(), ".json")
		var compact bytes.Buffer
		if err := json.Compact(&compact, d.Body); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		d.Body = compact.Bytes()
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Sign returns the request headers for a delivery, including the signature
// or token that the configured secrets would accept.
func Sign(d Delivery, secrets ingest.Secrets) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	for k, v := range d.Headers {
		h.Set(k, v)
	}
	switch d.Provider {
	case events.ProviderGitHub:
		h.Set("X-Hub-Signature-256", verify.Sign(secrets.GitHubWebhook, d.Body))
	case events.ProviderCI:
		h.Set("X-Mybuilds-Signature-256", verify.Sign(secrets.CIEvents, d.Body))
	case events.ProviderArgoCD:
		h.Set("Authorization", "Bearer "+secrets.ArgoCDToken)
	}
	return h
}

var rfc3339 = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)

// Shift moves every UTC timestamp in the delivery body by d, so a recorded
// release can be replayed as if it were happening now.
func Shift(dl Delivery, by time.Duration) Delivery {
	dl.Body = rfc3339.ReplaceAllFunc(dl.Body, func(m []byte) []byte {
		t, err := time.Parse(time.RFC3339Nano, string(m))
		if err != nil {
			return m
		}
		return []byte(t.Add(by).UTC().Format(time.RFC3339))
	})
	return dl
}

// ReplayOptions configures Replay.
type ReplayOptions struct {
	Secrets ingest.Secrets
	// Delay before the first delivery, so a browser can load the page.
	Lead time.Duration
	// MaxGap caps the wait between deliveries. Zero keeps the real gaps.
	MaxGap time.Duration
	Logger *slog.Logger
}

// Replay sends the recorded release through the ingester as a live event
// stream (plan section 10.4). Timestamps are shifted so the release starts
// now, and deliveries are spaced like the real ones. It returns when the
// last delivery is sent or ctx ends.
func Replay(ctx context.Context, in *ingest.Ingester, name string, o ReplayOptions) error {
	ds, err := Deliveries(name)
	if err != nil {
		return err
	}
	if len(ds) == 0 {
		return nil
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.Default()
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(o.Lead):
	}

	first := ds[0].OccurredAt()
	shift := time.Since(first).Truncate(time.Second)
	prev := first
	for _, d := range ds {
		at := d.OccurredAt()
		if at.IsZero() {
			at = prev
		}
		gap := at.Sub(prev)
		if o.MaxGap > 0 && gap > o.MaxGap {
			// Keep the timestamps honest: the shift grows by the time we
			// skip so the replayed release still ends "now".
			shift -= gap - o.MaxGap
			gap = o.MaxGap
		}
		prev = at
		if gap < 0 {
			gap = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(gap):
		}

		shifted := Shift(d, shift)
		res, err := in.Ingest(ctx, d.Provider, Sign(shifted, o.Secrets), shifted.Body)
		if err != nil {
			return fmt.Errorf("replay %s: %w", d.Name, err)
		}
		logger.Debug("fixture replay", "delivery", d.Name, "outcome", res.Outcome)
	}
	return nil
}
