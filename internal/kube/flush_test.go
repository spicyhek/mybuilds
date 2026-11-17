package kube

import (
	"context"
	"testing"
)

// TestRecordFlushesQueuedChangesOnStop: changes queued when the stop signal
// arrives are still recorded, as the pod that mybuilds' own release replaces
// needs (plan section 8.8).
func TestRecordFlushesQueuedChangesOnStop(t *testing.T) {
	h := newHarness(t)
	h.w.queue = make(chan change, 8)
	h.now = at(100)

	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	h.w.queue <- change{obj: deployment("12", 50, 49, 1, 0, 0, "ReplicaSetUpdated", 100)}
	h.w.queue <- change{obj: replicaSet("bbbbbbbbbb", "12", 101)}
	h.w.queue <- change{obj: pod("bbbbbbbbbb", "a", 101, 0, digestOf('b'))}

	// Returns once the queue is drained, well within the flush timeout.
	h.w.record(ctx)

	total, _, err := h.store.CountEvents(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("recorded %d events after stop, want 3", total)
	}
}
