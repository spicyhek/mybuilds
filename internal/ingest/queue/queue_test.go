package queue

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/ingest"
	"github.com/spicyhek/mybuilds/internal/ingest/ci"
	"github.com/spicyhek/mybuilds/internal/ingest/verify"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
)

const secret = "ci-secret"

// fakeAPI hands out one batch per receive and stops the consumer when it
// runs out.
type fakeAPI struct {
	batches [][]types.Message
	deleted []string
	stop    context.CancelFunc
}

func (f *fakeAPI) ReceiveMessage(_ context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	if aws.ToString(in.QueueUrl) == "" || in.WaitTimeSeconds != 20 || in.MaxNumberOfMessages != 10 || in.VisibilityTimeout != 60 {
		return nil, errors.New("unexpected receive input")
	}
	if len(f.batches) == 0 {
		f.stop()
		return &sqs.ReceiveMessageOutput{}, nil
	}
	b := f.batches[0]
	f.batches = f.batches[1:]
	return &sqs.ReceiveMessageOutput{Messages: b}, nil
}

func (f *fakeAPI) DeleteMessageBatch(_ context.Context, in *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	for _, e := range in.Entries {
		f.deleted = append(f.deleted, aws.ToString(e.Id))
	}
	return &sqs.DeleteMessageBatchOutput{}, nil
}

// flakyStore fails to store one release's events.
type flakyStore struct {
	*storage.Store
	failing string
}

func (s flakyStore) InsertEvent(ctx context.Context, e *events.Event) (bool, error) {
	if e.Keys.ReleaseID == s.failing {
		return false, errors.New("disk full")
	}
	return s.Store.InsertEvent(ctx, e)
}

func published(releaseID, sha string, run int) string {
	b, _ := json.Marshal(map[string]any{
		"type": "release.published", "release_id": releaseID,
		"service": "personal-site", "environment": "production",
		"source_repository": "spicyhek/homelab-hosted-personal-site", "source_sha": sha,
		"artifact":        "ghcr.io/spicyhek/homelab-hosted-personal-site",
		"artifact_digest": "sha256:" + strings.Repeat("a", 64),
		"workflow_run_id": 42, "workflow_run_number": run, "run_attempt": 1,
		"drill": false, "occurred_at": "2026-11-16T10:00:00Z",
	})
	return string(b)
}

func message(id string, env Envelope) types.Message {
	b, _ := json.Marshal(env)
	return types.Message{MessageId: aws.String(id), ReceiptHandle: aws.String("rh-" + id), Body: aws.String(string(b))}
}

func envelope(body, signature string) Envelope {
	return Envelope{
		Provider:   events.ProviderCI,
		Headers:    map[string]string{"content-type": "application/json", strings.ToLower(ci.SignatureHeader): signature},
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(body)),
		ReceivedAt: time.Now().Add(-2 * time.Second),
	}
}

func TestConsumerIngestsAndDeletesFinishedMessages(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	svcs, err := services.Load("")
	if err != nil {
		t.Fatal(err)
	}
	const broken = "personal-site/b00b00b/production/99"
	in := ingest.New(ingest.Options{
		Store:    flakyStore{Store: store, failing: broken},
		Services: svcs,
		Secrets:  ingest.Secrets{CIEvents: secret},
	})

	good := published("personal-site/ce61154/production/11", "ce61154e05756b0a7e40311de30dccaa0273f305", 11)
	bad := published(broken, "b00b00b"+strings.Repeat("0", 33), 99)
	run, stop := context.WithCancel(ctx)
	defer stop()
	api := &fakeAPI{stop: stop, batches: [][]types.Message{
		{
			message("stored", envelope(good, verify.Sign(secret, []byte(good)))),
			message("duplicate", envelope(good, verify.Sign(secret, []byte(good)))),
			message("unsigned", envelope(good, verify.Sign("other", []byte(good)))),
			{MessageId: aws.String("garbage"), ReceiptHandle: aws.String("rh"), Body: aws.String("not json")},
			message("unstorable", envelope(bad, verify.Sign(secret, []byte(bad)))),
		},
	}}
	c, err := New(Options{QueueURL: "https://sqs.us-west-1.amazonaws.com/123456789012/q", Ingester: in, Client: api})
	if err != nil {
		t.Fatal(err)
	}
	c.Run(run)

	want := []string{"stored", "duplicate", "unsigned", "garbage"}
	if strings.Join(api.deleted, ",") != strings.Join(want, ",") {
		t.Errorf("deleted %v, want %v", api.deleted, want)
	}
	total, _, err := store.CountEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("stored %d events, want 1", total)
	}
}

func TestRegionFromURL(t *testing.T) {
	if r, err := RegionFromURL("https://sqs.us-west-1.amazonaws.com/123456789012/mybuilds-ingest"); err != nil || r != "us-west-1" {
		t.Errorf("region = %q, %v", r, err)
	}
	for _, bad := range []string{"https://example.com/q", "https://sqs..amazonaws.com/1/q", "::"} {
		if _, err := RegionFromURL(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestNewNeedsCredentialsWithoutAClient(t *testing.T) {
	in := ingest.New(ingest.Options{})
	if _, err := New(Options{QueueURL: "https://sqs.us-west-1.amazonaws.com/1/q", Ingester: in}); err == nil {
		t.Error("no error without an access key")
	}
	if _, err := New(Options{Ingester: in}); err == nil {
		t.Error("no error without a queue URL")
	}
	if _, err := New(Options{QueueURL: "https://sqs.us-west-1.amazonaws.com/1/q", Ingester: in, AccessKeyID: "k", SecretAccessKey: "s"}); err != nil {
		t.Errorf("real client: %v", err)
	}
}
