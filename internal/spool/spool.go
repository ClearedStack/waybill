// Package spool stages worker outcomes on disk under /var/spool/cs before
// acknowledging leases to Muster, so compiled specifications survive transient
// timeouts and Muster restarts without losing work.
package spool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/strictjson"
)

// Actions staged for Muster.
const (
	ActionComplete     = "complete"
	ActionFail         = "fail"
	ActionNeedsSubtool = "needs_subtool"
)

// MaxEntryBytes bounds a single staged outcome file so Waybill stays within
// Muster's 256 KiB complete body limit and its 30 MiB memory budget.
const MaxEntryBytes = 256 << 10

// Entry is one pending worker call to deliver to Muster.
type Entry struct {
	Action      string          `json:"action"`
	Lease       string          `json:"lease"`
	ItemID      string          `json:"item_id,omitempty"`
	State       string          `json:"state,omitempty"`
	Code        fault.Code      `json:"code,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Fingerprint string          `json:"fingerprint,omitempty"`
}

// Staged pairs a filename in the outbox with its decoded Entry.
type Staged struct {
	Name  string
	Entry Entry
}

// Store manages the atomic outbox directory inside /var/spool/cs.
type Store struct {
	mu     sync.Mutex
	outbox string
	seq    uint64
}

// Open initializes the outbox inside root, removes incomplete temporary files
// left by an interrupted write, and recovers the next sequence number.
func Open(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("spool: empty directory")
	}
	outbox := filepath.Join(root, "outbox")
	if err := os.MkdirAll(outbox, 0o700); err != nil {
		return nil, errors.New("spool: could not create outbox")
	}
	entries, err := os.ReadDir(outbox)
	if err != nil {
		return nil, errors.New("spool: could not read outbox")
	}
	var maxSeq uint64
	for _, de := range entries {
		name := de.Name()
		if strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(filepath.Join(outbox, name))
			continue
		}
		if numText, ok := strings.CutSuffix(name, ".json"); ok {
			if n, err := strconv.ParseUint(numText, 10, 64); err == nil && n > maxSeq {
				maxSeq = n
			}
		}
	}
	return &Store{outbox: outbox, seq: maxSeq}, nil
}

// Enqueue validates e and writes it atomically to the outbox in FIFO order.
func (s *Store) Enqueue(e Entry) (string, error) {
	if err := validateEntry(e); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return "", errors.New("spool: could not encode entry")
	}
	data := buf.Bytes()
	if len(data) > MaxEntryBytes {
		return "", errors.New("spool: entry exceeds size limit")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	name := fmt.Sprintf("%016d.json", s.seq)
	finalPath := filepath.Join(s.outbox, name)
	tmpPath := finalPath + ".tmp"

	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", errors.New("spool: could not open temporary file")
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return "", errors.New("spool: could not write temporary file")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return "", errors.New("spool: could not sync temporary file")
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", errors.New("spool: could not close temporary file")
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", errors.New("spool: could not commit staged file")
	}
	if dir, err := os.Open(s.outbox); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return name, nil
}

// List returns all staged entries in FIFO order.
func (s *Store) List() ([]Staged, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.outbox)
	if err != nil {
		return nil, errors.New("spool: could not list outbox")
	}
	var names []string
	for _, de := range entries {
		if !de.IsDir() && strings.HasSuffix(de.Name(), ".json") {
			names = append(names, de.Name())
		}
	}
	sort.Strings(names)

	out := make([]Staged, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(s.outbox, name))
		if err != nil {
			return nil, errors.New("spool: could not read staged file")
		}
		if len(raw) > MaxEntryBytes {
			return nil, errors.New("spool: staged file exceeds size limit")
		}
		var e Entry
		if err := strictjson.Decode(raw, &e); err != nil {
			return nil, errors.New("spool: staged file is malformed")
		}
		if err := validateEntry(e); err != nil {
			return nil, err
		}
		out = append(out, Staged{Name: name, Entry: e})
	}
	return out, nil
}

// Remove deletes a staged entry once Muster has acknowledged or terminally
// refused it. Removing an already-absent file is a no-op.
func (s *Store) Remove(name string) error {
	if name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return errors.New("spool: invalid entry name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.Remove(filepath.Join(s.outbox, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("spool: could not remove staged file")
	}
	return nil
}

func validateEntry(e Entry) error {
	if e.Lease == "" {
		return errors.New("spool: lease is required")
	}
	switch e.Action {
	case ActionComplete:
		if e.State == "" {
			return errors.New("spool: state is required for complete")
		}
		if len(e.Result) > 0 {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(e.Result, &obj); err != nil || obj == nil {
				return errors.New("spool: complete result must be a JSON object")
			}
		}
	case ActionFail:
		if e.Code != fault.Parse && e.Code != fault.Timeout {
			return errors.New("spool: fail requires E_PARSE or E_TIMEOUT")
		}
	case ActionNeedsSubtool:
	default:
		return errors.New("spool: unknown action")
	}
	return nil
}
