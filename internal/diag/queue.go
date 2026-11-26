package diag

import (
	"cmp"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Queue limits: the oldest reports go first.
const (
	MaxQueued      = 50
	MaxQueuedBytes = 2 << 20
	// SameWindow is how close two reports of one problem must be to count as
	// one: a goroutine's panic that Go records and the crash it then causes.
	SameWindow = 600 // seconds
)

// Queue is the directory of reports waiting to be sent, one JSON file each.
// They are redacted before they are written: what is here is what is sent.
type Queue struct{ Dir string }

var queueMu sync.Mutex // one process writes its queue; this keeps its goroutines apart

// Add writes r to the queue, unless a report of the same problem from the same
// process is already there from around the same time. It reports whether r
// was added.
func (q Queue) Add(r Report) (bool, error) {
	if err := r.Check(); err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return false, err
	}
	if len(data) > MaxReport {
		r.Context = nil
		if data, err = json.MarshalIndent(r, "", "  "); err != nil {
			return false, err
		}
	}
	queueMu.Lock()
	defer queueMu.Unlock()
	if err := os.MkdirAll(q.Dir, 0o700); err != nil {
		return false, err
	}
	have, _ := q.list()
	for _, o := range have {
		if o.Fingerprint == r.Fingerprint && o.Process == r.Process && abs(o.OccurredAt-r.OccurredAt) < SameWindow {
			return false, nil
		}
	}
	tmp := filepath.Join(q.Dir, "."+r.ID+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, filepath.Join(q.Dir, r.ID+".json")); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	q.trim()
	return true, nil
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// trim drops the oldest reports beyond the limits. Caller holds queueMu.
func (q Queue) trim() {
	ents, err := os.ReadDir(q.Dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		size int64
		mod  int64
	}
	var files []file
	var total int64
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, file{e.Name(), info.Size(), info.ModTime().UnixNano()})
			total += info.Size()
		}
	}
	slices.SortFunc(files, func(a, b file) int { return cmp.Compare(a.mod, b.mod) })
	for len(files) > 0 && (len(files) > MaxQueued || total > MaxQueuedBytes) {
		_ = os.Remove(filepath.Join(q.Dir, files[0].name))
		total -= files[0].size
		files = files[1:]
	}
}

// List returns the queued reports, oldest first.
func (q Queue) List() ([]Report, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	return q.list()
}

func (q Queue) list() ([]Report, error) {
	ents, err := os.ReadDir(q.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Report{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Report{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(q.Dir, e.Name()))
		if err != nil {
			continue
		}
		var r Report
		if json.Unmarshal(data, &r) != nil || r.ID+".json" != e.Name() {
			_ = os.Remove(filepath.Join(q.Dir, e.Name())) // not one of ours, or damaged
			continue
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Report) int { return cmp.Compare(a.OccurredAt, b.OccurredAt) })
	return out, nil
}

// Remove deletes the report id from the queue.
func (q Queue) Remove(id string) error {
	if !idRE.MatchString(id) {
		return ErrInvalid
	}
	queueMu.Lock()
	defer queueMu.Unlock()
	err := os.Remove(filepath.Join(q.Dir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Clear empties the queue.
func (q Queue) Clear() error {
	queueMu.Lock()
	defer queueMu.Unlock()
	ents, err := os.ReadDir(q.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".tmp") {
			if err := os.Remove(filepath.Join(q.Dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
