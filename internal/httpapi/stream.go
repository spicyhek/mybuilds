package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/storage"
)

// SSE message types (plan section 10.3).
const (
	msgHello          = "hello"
	msgReleaseCreated = "release.created"
	msgReleaseUpdated = "release.updated"
	// msgSnapshot asks the client to refetch from REST because the cursor
	// it resumed from is too old to replay.
	msgSnapshot = "snapshot"
)

// defaultReplayLimit bounds how many missed events a reconnecting client
// gets replayed before being told to refetch a snapshot instead.
const defaultReplayLimit = 500

// heartbeatInterval keeps idle connections open through proxies.
const heartbeatInterval = 25 * time.Second

// message is one SSE frame.
type message struct {
	ID   int64
	Type string
	Data any
}

// HubStore is what the stream needs from storage.
type HubStore interface {
	GetRelease(ctx context.Context, releaseID string) (*releases.Release, error)
	EventsAfter(ctx context.Context, afterID int64, limit int) ([]events.Event, error)
	LastEventID(ctx context.Context) (int64, error)
	AffectedReleaseIDs(ctx context.Context, e events.Event) ([]string, error)
}

// Hub fans projected releases out to SSE subscribers.
type Hub struct {
	store  HubStore
	logger *slog.Logger
	// ReplayLimit is the most missed events replayed to a reconnecting
	// client; beyond it the client is told to refetch a snapshot.
	ReplayLimit int

	mu   sync.Mutex
	subs map[chan message]struct{}
}

// NewHub returns a Hub reading release views from store.
func NewHub(store HubStore, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{store: store, logger: logger, ReplayLimit: defaultReplayLimit, subs: map[chan message]struct{}{}}
}

// Notify publishes the releases an event re-projected. It is the ingester's
// Notify callback.
func (h *Hub) Notify(ctx context.Context, eventID int64, outcomes []releases.Outcome) {
	for _, o := range outcomes {
		rel, err := h.store.GetRelease(ctx, o.ReleaseID)
		if err != nil {
			h.logger.Error("stream: load release", "release_id", o.ReleaseID, "error", err)
			continue
		}
		typ := msgReleaseUpdated
		if o.Created {
			typ = msgReleaseCreated
		}
		h.broadcast(message{ID: eventID, Type: typ, Data: toView(*rel)})
	}
}

// Subscribers returns how many clients are connected.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *Hub) subscribe() (chan message, func()) {
	ch := make(chan message, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *Hub) broadcast(m message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- m:
		default:
			// A client that cannot keep up is dropped; it reconnects with
			// Last-Event-ID and gets a replay or a snapshot.
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// stream is GET /api/v1/stream.
func (s *server) stream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := http.NewResponseController(w)

	// Subscribe before reading the cursor so nothing falls between replay
	// and live delivery; a duplicate is harmless, a gap is not.
	ch, unsubscribe := s.hub.subscribe()
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		// No streaming support (or the client is gone): nothing to do.
		s.logger.Warn("stream: flush", "error", err)
		return
	}

	last, err := s.hub.store.LastEventID(ctx)
	if err != nil {
		s.logger.Error("stream: last event id", "error", err)
		return
	}
	send := func(m message) bool {
		if err := writeSSE(w, m); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send(message{ID: last, Type: msgHello, Data: map[string]any{
		"last_event_id": last, "release_id": s.releaseID, "version": s.version,
	}}) {
		return
	}

	if cursor, ok := parseCursor(r.Header.Get("Last-Event-ID")); ok && cursor < last {
		if !s.replay(ctx, cursor, send) {
			return
		}
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case m, ok := <-ch:
			if !ok || !send(m) {
				return
			}
		}
	}
}

// replay sends the releases touched by events after cursor, or a snapshot
// request when there are too many to replay.
func (s *server) replay(ctx context.Context, cursor int64, send func(message) bool) bool {
	limit := s.hub.ReplayLimit
	evs, err := s.hub.store.EventsAfter(ctx, cursor, limit+1)
	if err != nil {
		s.logger.Error("stream: replay", "cursor", cursor, "error", err)
		return send(message{Type: msgSnapshot, Data: map[string]string{"reason": "replay failed"}})
	}
	if len(evs) > limit {
		return send(message{ID: evs[len(evs)-1].ID, Type: msgSnapshot, Data: map[string]string{"reason": "cursor too old"}})
	}

	touched := map[string]int64{}
	var order []string
	for _, e := range evs {
		ids, err := s.hub.store.AffectedReleaseIDs(ctx, e)
		if err != nil {
			s.logger.Error("stream: replay", "event_id", e.ID, "error", err)
			continue
		}
		for _, id := range ids {
			if _, seen := touched[id]; !seen {
				order = append(order, id)
			}
			touched[id] = e.ID
		}
	}
	for _, id := range order {
		rel, err := s.hub.store.GetRelease(ctx, id)
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			s.logger.Error("stream: replay", "release_id", id, "error", err)
			continue
		}
		if !send(message{ID: touched[id], Type: msgReleaseUpdated, Data: toView(*rel)}) {
			return false
		}
	}
	return true
}

func parseCursor(header string) (int64, bool) {
	if header == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(header, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func writeSSE(w http.ResponseWriter, m message) error {
	data, err := json.Marshal(m.Data)
	if err != nil {
		return err
	}
	if m.ID > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", m.ID); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.Type, data)
	return err
}
