// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

// Package audit records who changed what in GPU Lens: live threshold edits, connection changes, collection
// start/stop, alert tests and simulation scenario switches, including attempts that were refused.
//
// Entries are kept in memory (the most recent 1000) and, when a file is configured, appended to it as JSON lines
// so the record survives restarts. Secrets (tokens, passwords) are never written.
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

// Outcomes.
const (
	OutcomeOK     = "ok"
	OutcomeDenied = "denied"
	OutcomeError  = "error"
)

const maxEntries = 1000

// Entry is one audited action.
type Entry struct {
	Seq     int       `json:"seq"`
	T       time.Time `json:"t"`
	Actor   string    `json:"actor"`
	Role    string    `json:"role"`
	Remote  string    `json:"remote"`
	Action  string    `json:"action"`
	Detail  string    `json:"detail"`
	Outcome string    `json:"outcome"`
}

// Log is a bounded, optionally file-backed audit log. A nil *Log discards everything.
type Log struct {
	mu      sync.Mutex
	entries []Entry
	seq     int
	path    string
	now     func() time.Time
}

// New opens an audit log. With a path, earlier entries are loaded from the file and new ones are appended to it.
func New(path string) (*Log, error) {
	l := &Log{path: path, now: func() time.Time { return time.Now().UTC() }}
	if path == "" {
		return l, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return l, l.touch()
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Seq > 0 {
			l.entries = append(l.entries, e)
			if e.Seq > l.seq {
				l.seq = e.Seq
			}
			if len(l.entries) > maxEntries {
				l.entries = l.entries[len(l.entries)-maxEntries:]
			}
		}
	}
	return l, nil
}

func (l *Log) touch() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// Record appends an entry and returns it with its sequence number and time filled in.
func (l *Log) Record(e Entry) Entry {
	if l == nil {
		return e
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.Seq = l.seq
	if e.T.IsZero() {
		e.T = l.now()
	}
	if e.Outcome == "" {
		e.Outcome = OutcomeOK
	}
	l.entries = append(l.entries, e)
	if len(l.entries) > maxEntries {
		l.entries = append([]Entry(nil), l.entries[len(l.entries)-maxEntries:]...)
	}
	if l.path != "" {
		if f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			b, _ := json.Marshal(e)
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
	return e
}

// List returns entries newer than `since`, oldest first.
func (l *Log) List(since int) []Entry {
	if l == nil {
		return []Entry{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []Entry{}
	for _, e := range l.entries {
		if e.Seq > since {
			out = append(out, e)
		}
	}
	return out
}

// Path is the backing file, or "" when the log is memory-only.
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
