package events

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Recording is a release's exported events as a test fixture (plan section
// 7.5). On disk it is JSON Lines: a header line, then one Record per line in
// ID order, so a re-recording diffs line by line.
type Recording struct {
	ReleaseID string `json:"release_id"`
	// RecordedFrom is the mybuilds instance the events came from.
	RecordedFrom string    `json:"recorded_from,omitempty"`
	RecordedAt   time.Time `json:"recorded_at"`
	Events       []Record  `json:"-"`
}

// WriteRecording writes rec as JSON Lines.
func WriteRecording(w io.Writer, rec Recording) error {
	if rec.ReleaseID == "" {
		return errors.New("recording has no release ID")
	}
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		return err
	}
	for _, r := range rec.Events {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("event %d: %w", r.ID, err)
		}
	}
	return bw.Flush()
}

// ReadRecording reads a recording written by WriteRecording.
func ReadRecording(r io.Reader) (Recording, error) {
	var rec Recording
	sc := bufio.NewScanner(r)
	// A workflow_job with every step is a few kilobytes; leave room.
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	line := 0
	for sc.Scan() {
		line++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		if line == 1 {
			if err := json.Unmarshal(b, &rec); err != nil {
				return rec, fmt.Errorf("header: %w", err)
			}
			continue
		}
		var e Record
		if err := json.Unmarshal(b, &e); err != nil {
			return rec, fmt.Errorf("line %d: %w", line, err)
		}
		rec.Events = append(rec.Events, e)
	}
	if err := sc.Err(); err != nil {
		return rec, err
	}
	if rec.ReleaseID == "" {
		return rec, errors.New("recording has no release ID")
	}
	return rec, nil
}
