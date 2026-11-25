package edits

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
)

// Source is the read-only view of one user's uploads.
type Source interface {
	// Original returns the uploaded bytes of name. ok is false when no
	// file of that name was uploaded. editable is false for files the
	// model only sees as extracted text (PDF, DOCX), images and binaries;
	// reason says why.
	Original(name string) (data []byte, ok, editable bool, reason string)
	// IsDir reports whether name is an uploaded directory.
	IsDir(name string) bool
	// Ignored reports whether name has an ignored component (.git).
	Ignored(name string) bool
}

// Rules limit what one reply may change.
type Rules struct {
	AllowNew    bool
	AllowDelete bool
	// MaxFiles is the number of files one reply may touch.
	MaxFiles int
	// MaxBytes caps the size of a changed file.
	MaxBytes int64
	// Seen holds the files the model saw complete in the prompt. A WHOLE
	// block over an existing file is only accepted for those, so a file the
	// model saw truncated is never replaced by the part it saw.
	Seen map[string]bool
}

// Status of a proposed change.
type Status int

const (
	StatusPending Status = iota
	StatusAccepted
	StatusRejected
	StatusFailed
	// StatusSuperseded is a pending change replaced by a newer proposal.
	StatusSuperseded
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusAccepted:
		return "accepted"
	case StatusRejected:
		return "rejected"
	case StatusFailed:
		return "failed"
	case StatusSuperseded:
		return "superseded"
	}
	return "unknown"
}

// Failure is a block that could not be applied, or could not be parsed.
type Failure struct {
	Path   string
	Kind   Kind
	Block  int // 1-based position among the reply's blocks for this file
	Line   int // line of the block in the reply
	Reason string
	// Near are the file lines closest to a SEARCH that did not match.
	Near   []string
	NearAt int
	Parse  bool
}

// Change is the proposal for one file from one reply.
type Change struct {
	ID            int
	Path          string
	Before, After string
	BeforeExists  bool
	AfterExists   bool
	Status        Status
	Reason        string
	Blocks        int
	Applied       int
	Failures      []Failure
	Added         int
	Removed       int
}

// New reports whether the change creates a file.
func (c Change) New() bool { return !c.BeforeExists && c.AfterExists }

// Deletes reports whether the change removes a file.
func (c Change) Deletes() bool { return c.BeforeExists && !c.AfterExists }

type version struct {
	text   string
	exists bool
}

type fileState struct {
	path       string
	base       string
	baseExists bool
	cur        version
	history    []version
	change     *Change
}

func (f *fileState) modified() bool {
	return f.cur.exists != f.baseExists || (f.cur.exists && f.cur.text != f.base)
}

// Workspace holds one user's edited versions. Safe for concurrent use by
// several TUI sessions of the same user.
type Workspace struct {
	mu       sync.Mutex
	files    map[string]*fileState
	nextID   int
	failures []Failure
}

func newWorkspace() *Workspace { return &Workspace{files: map[string]*fileState{}} }

// Result summarises one Propose call.
type Result struct {
	Changes  []Change
	Failures []Failure
}

// Files is the number of files with a change to review.
func (r Result) Files() int {
	n := 0
	for _, c := range r.Changes {
		if c.Status == StatusPending {
			n++
		}
	}
	return n
}

// NewFiles is the number of pending changes that create a file.
func (r Result) NewFiles() int {
	n := 0
	for _, c := range r.Changes {
		if c.Status == StatusPending && c.New() {
			n++
		}
	}
	return n
}

// Empty reports whether the reply had no blocks at all.
func (r Result) Empty() bool { return len(r.Changes) == 0 && len(r.Failures) == 0 }

var reservedTop = map[string]bool{"uploads": true, "downloads": true}

// resolvePath cleans a path from the reply. Paths are relative to the
// upload directory; a leading "/", "./" or "uploads/" is dropped when the
// result names an uploaded file and the original does not.
func resolvePath(src Source, p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	p = strings.TrimLeft(p, "/")
	clean, err := uploads.CleanPath(p)
	if err != nil {
		return "", err
	}
	if rest, ok := strings.CutPrefix(clean, "uploads/"); ok {
		if _, exists, _, _ := src.Original(clean); !exists {
			if _, exists, _, _ := src.Original(rest); exists {
				return rest, nil
			}
		}
	}
	return clean, nil
}

// Propose applies the parsed blocks of one reply to the current versions
// and records a pending change per file. Nothing is accepted here.
func (w *Workspace) Propose(src Source, blocks []Block, parseErrs []ParseError, rules Rules) Result {
	w.mu.Lock()
	defer w.mu.Unlock()
	var res Result
	for _, e := range parseErrs {
		res.Failures = append(res.Failures, Failure{Path: e.Path, Line: e.Line, Reason: e.Reason, Parse: true})
	}
	type group struct {
		path   string
		raw    string
		err    error
		blocks []Block
	}
	var groups []*group
	byPath := map[string]*group{}
	for _, b := range blocks {
		clean, err := resolvePath(src, b.Path)
		key := clean
		if err != nil {
			key = "\x00" + b.Path
		}
		g := byPath[key]
		if g == nil {
			g = &group{path: clean, raw: b.Path, err: err}
			byPath[key] = g
			groups = append(groups, g)
		}
		g.blocks = append(g.blocks, b)
	}
	for i, g := range groups {
		ch := w.proposeFile(src, g.path, g.raw, g.err, g.blocks, rules, i)
		res.Changes = append(res.Changes, ch)
		res.Failures = append(res.Failures, ch.Failures...)
	}
	w.failures = res.Failures
	return res
}

func (w *Workspace) proposeFile(src Source, p, raw string, perr error, blocks []Block, rules Rules, index int) Change {
	w.nextID++
	ch := Change{ID: w.nextID, Path: p, Blocks: len(blocks)}
	if p == "" {
		ch.Path = raw
	}
	fail := func(reason string) Change {
		ch.Status = StatusFailed
		ch.Reason = reason
		for i, b := range blocks {
			ch.Failures = append(ch.Failures, Failure{Path: ch.Path, Kind: b.Kind, Block: i + 1, Line: b.Line, Reason: reason})
		}
		return ch
	}
	if perr != nil {
		return fail("invalid path: " + perr.Error())
	}
	if rules.MaxFiles > 0 && index >= rules.MaxFiles {
		return fail(fmt.Sprintf("more than %d files in one reply (EDIT_MAX_FILES)", rules.MaxFiles))
	}
	if src.Ignored(p) {
		return fail("path is under an ignored directory (UPLOAD_IGNORE)")
	}
	if src.IsDir(p) {
		return fail("path is a directory")
	}
	st := w.files[p]
	data, uploaded, editable, why := src.Original(p)
	if uploaded && !editable {
		return fail("not an editable text file: " + why)
	}
	if st == nil {
		st = &fileState{path: p, base: string(data), baseExists: uploaded}
		st.cur = version{text: st.base, exists: uploaded}
	} else if len(st.history) == 0 && !st.modified() {
		// Nothing accepted yet: follow a re-uploaded original.
		st.base, st.baseExists = string(data), uploaded
		st.cur = version{text: st.base, exists: uploaded}
	}
	ch.Before, ch.BeforeExists = st.cur.text, st.cur.exists
	work := version{text: st.cur.text, exists: st.cur.exists}
	top, _, _ := strings.Cut(p, "/")
	for i, b := range blocks {
		reason, near, nearAt := applyBlock(&work, b, rules, p, top, st.cur.exists)
		if reason == "" {
			ch.Applied++
			continue
		}
		ch.Failures = append(ch.Failures, Failure{Path: p, Kind: b.Kind, Block: i + 1, Line: b.Line, Reason: reason, Near: near, NearAt: nearAt})
	}
	ch.After, ch.AfterExists = work.text, work.exists
	switch {
	case ch.Applied == 0:
		ch.Status = StatusFailed
		ch.Reason = ch.Failures[0].Reason
	case ch.AfterExists == ch.BeforeExists && ch.After == ch.Before:
		ch.Status = StatusFailed
		ch.Reason = "the blocks do not change the file"
	case rules.MaxBytes > 0 && int64(len(ch.After)) > rules.MaxBytes:
		ch.Status = StatusFailed
		ch.Reason = fmt.Sprintf("result is %d bytes, over the per-file limit of %d bytes (UPLOAD_MAX_BYTES)", len(ch.After), rules.MaxBytes)
	default:
		ch.Status = StatusPending
		if len(ch.Failures) > 0 {
			ch.Reason = fmt.Sprintf("%d of %d blocks applied", ch.Applied, ch.Blocks)
		}
	}
	if ch.Status == StatusFailed && len(ch.Failures) == 0 {
		ch.Failures = append(ch.Failures, Failure{Path: p, Block: 1, Line: blocks[0].Line, Reason: ch.Reason})
	}
	ch.Added, ch.Removed = Stats(ch.Before, ch.After)
	if old := st.change; old != nil && old.Status == StatusPending {
		old.Status = StatusSuperseded
	}
	stored := ch
	st.change = &stored
	w.files[p] = st
	return ch
}

// applyBlock changes work in place, or returns why it could not.
func applyBlock(work *version, b Block, rules Rules, p, top string, existedBefore bool) (string, []string, int) {
	switch b.Kind {
	case KindDelete:
		if !rules.AllowDelete {
			return "deleting files is turned off (EDIT_ALLOW_DELETE=false)", nil, 0
		}
		if !work.exists {
			return "file does not exist", nil, 0
		}
		work.exists = false
		work.text = ""
		return "", nil, 0
	case KindWhole:
		if work.exists {
			if existedBefore && !rules.Seen[p] {
				return "WHOLE needs the complete file, and it was not fully in the prompt (truncated or excluded); use SEARCH/REPLACE", nil, 0
			}
			old := splitText(work.text)
			t := text{lines: b.Replace, crlf: old.crlf, final: old.final || len(old.lines) == 0}
			if len(b.Replace) == 0 {
				t.lines = nil
			}
			work.text = t.String()
			return "", nil, 0
		}
		if reason := newFileCheck(rules, p, top); reason != "" {
			return reason, nil, 0
		}
		t := text{lines: b.Replace, final: true}
		work.text, work.exists = t.String(), true
		return "", nil, 0
	default:
		if !work.exists {
			if len(trimBlank(b.Search)) == 0 {
				if reason := newFileCheck(rules, p, top); reason != "" {
					return reason, nil, 0
				}
				t := text{lines: b.Replace, final: true}
				work.text, work.exists = t.String(), true
				return "", nil, 0
			}
			return "file does not exist; use WHOLE to create it", nil, 0
		}
		if len(trimBlank(b.Search)) == 0 {
			return "empty SEARCH; quote the lines to change, or use WHOLE", nil, 0
		}
		t := splitText(work.text)
		lines, err := replaceLines(t.lines, b.Search, b.Replace)
		if err != nil {
			var me *MatchError
			if errors.As(err, &me) {
				return me.Error(), me.Near, me.NearAt
			}
			return err.Error(), nil, 0
		}
		t.lines = lines
		if len(t.lines) > 0 && len(splitText(work.text).lines) == 0 {
			t.final = true
		}
		work.text = t.String()
		return "", nil, 0
	}
}

func newFileCheck(rules Rules, p, top string) string {
	if !rules.AllowNew {
		return "creating files is turned off (EDIT_ALLOW_NEW=false)"
	}
	if reservedTop[top] {
		return fmt.Sprintf("%q is a reserved top-level name", top)
	}
	return ""
}

// ErrNoChange is returned by Accept, Reject, Undo and Revert when there is
// nothing to do for the file.
var ErrNoChange = errors.New("nothing to do")

// ErrStale means the file changed after the proposal was made.
var ErrStale = errors.New("the file changed after this proposal; ask the model again")

// Accept makes the pending change of p the current version.
func (w *Workspace) Accept(p string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.acceptLocked(p)
}

func (w *Workspace) acceptLocked(p string) error {
	st := w.files[p]
	if st == nil || st.change == nil || st.change.Status != StatusPending {
		return ErrNoChange
	}
	ch := st.change
	if st.cur.exists != ch.BeforeExists || st.cur.text != ch.Before {
		return ErrStale
	}
	st.history = append(st.history, st.cur)
	st.cur = version{text: ch.After, exists: ch.AfterExists}
	ch.Status = StatusAccepted
	return nil
}

// Reject drops the pending change of p.
func (w *Workspace) Reject(p string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.files[p]
	if st == nil || st.change == nil || st.change.Status != StatusPending {
		return ErrNoChange
	}
	st.change.Status = StatusRejected
	return nil
}

// AcceptAll accepts every pending change and returns how many it took.
func (w *Workspace) AcceptAll() (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	var firstErr error
	for _, p := range w.sortedLocked() {
		st := w.files[p]
		if st.change == nil || st.change.Status != StatusPending {
			continue
		}
		if err := w.acceptLocked(p); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", p, err)
			}
			continue
		}
		n++
	}
	return n, firstErr
}

// RejectAll rejects every pending change.
func (w *Workspace) RejectAll() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, st := range w.files {
		if st.change != nil && st.change.Status == StatusPending {
			st.change.Status = StatusRejected
			n++
		}
	}
	return n
}

// Undo restores the version of p before the last accept or revert. When
// that undoes the latest accepted change, the change is pending again.
func (w *Workspace) Undo(p string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.files[p]
	if st == nil || len(st.history) == 0 {
		return ErrNoChange
	}
	prev := st.history[len(st.history)-1]
	st.history = st.history[:len(st.history)-1]
	if ch := st.change; ch != nil && ch.Status == StatusAccepted &&
		st.cur.exists == ch.AfterExists && st.cur.text == ch.After &&
		prev.exists == ch.BeforeExists && prev.text == ch.Before {
		ch.Status = StatusPending
	}
	st.cur = prev
	return nil
}

// Revert sets p back to the uploaded original. Undo brings the edited
// version back.
func (w *Workspace) Revert(p string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.files[p]
	if st == nil || !st.modified() {
		return ErrNoChange
	}
	st.history = append(st.history, st.cur)
	st.cur = version{text: st.base, exists: st.baseExists}
	if st.change != nil && st.change.Status == StatusPending {
		st.change.Status = StatusSuperseded
	}
	return nil
}

// Entry is one file on the review screen.
type Entry struct {
	Path string
	// Change is the latest proposal for the file.
	Change Change
	// Modified is true when the current version differs from the upload.
	Modified   bool
	New        bool
	Deleted    bool
	CanUndo    bool
	Added      int // current version against the upload
	Removed    int
	BaseExists bool
}

// Entries lists every file with a proposal or an accepted change, sorted.
func (w *Workspace) Entries() []Entry {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []Entry
	for _, p := range w.sortedLocked() {
		st := w.files[p]
		e := Entry{Path: p, Modified: st.modified(), CanUndo: len(st.history) > 0, BaseExists: st.baseExists}
		if st.change != nil {
			e.Change = *st.change
		}
		e.New = !st.baseExists && st.cur.exists
		e.Deleted = st.baseExists && !st.cur.exists
		if e.Modified {
			e.Added, e.Removed = Stats(versionText(st.baseExists, st.base), versionText(st.cur.exists, st.cur.text))
		}
		out = append(out, e)
	}
	return out
}

func versionText(exists bool, s string) string {
	if !exists {
		return ""
	}
	return s
}

func (w *Workspace) sortedLocked() []string {
	out := make([]string, 0, len(w.files))
	for p := range w.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Diff returns what the review screen shows for p: the pending proposal
// against the current version, or else the current version against the
// upload. title names which one it is.
func (w *Workspace) Diff(p string) (old, new string, title string, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.files[p]
	if st == nil {
		return "", "", "", false
	}
	if ch := st.change; ch != nil && ch.Status == StatusPending {
		return ch.Before, ch.After, "proposed change", true
	}
	return versionText(st.baseExists, st.base), versionText(st.cur.exists, st.cur.text), "accepted changes against the upload", true
}

// Compare returns the uploaded text of p and its newest version: the
// result of a pending proposal, or else the accepted text. pending tells
// which. current is the accepted text, the base of a pending proposal.
func (w *Workspace) Compare(p string) (orig, newest, current string, pending, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.files[p]
	if st == nil {
		return "", "", "", false, false
	}
	orig = versionText(st.baseExists, st.base)
	current = versionText(st.cur.exists, st.cur.text)
	if ch := st.change; ch != nil && ch.Status == StatusPending {
		return orig, versionText(ch.AfterExists, ch.After), current, true, true
	}
	return orig, current, current, false, true
}

// Changed returns every file whose current version differs from the
// upload, sorted by path.
func (w *Workspace) Changed() []FileDiff {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []FileDiff
	for _, p := range w.sortedLocked() {
		st := w.files[p]
		if !st.modified() {
			continue
		}
		out = append(out, FileDiff{Path: p, Old: st.base, New: st.cur.text, OldExists: st.baseExists, NewExists: st.cur.exists})
	}
	return out
}

// Pending is the number of changes waiting for review.
func (w *Workspace) Pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, st := range w.files {
		if st.change != nil && st.change.Status == StatusPending {
			n++
		}
	}
	return n
}

// Modified reports whether p has accepted changes.
func (w *Workspace) Modified(p string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.files[p]
	return st != nil && st.modified()
}

// Failures are the failed blocks of the latest reply.
func (w *Workspace) Failures() []Failure {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Failure(nil), w.failures...)
}

// ClearFailures forgets the failures once they were sent back.
func (w *Workspace) ClearFailures() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failures = nil
}

// Version implements uploads.Overlay: the accepted text of an edited file.
func (w *Workspace) Version(name string) (string, bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.files[name]
	if st == nil || !st.modified() {
		return "", false, false
	}
	return st.cur.text, st.cur.exists, true
}

// Added implements uploads.Overlay: accepted new files, sorted.
func (w *Workspace) Added() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, p := range w.sortedLocked() {
		st := w.files[p]
		if !st.baseExists && st.cur.exists {
			out = append(out, p)
		}
	}
	return out
}

// Current returns the current text of p and whether it exists.
func (w *Workspace) Current(p string) (string, bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.files[p]
	if st == nil {
		return "", false, false
	}
	return st.cur.text, st.cur.exists, true
}

// Reset drops everything.
func (w *Workspace) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files = map[string]*fileState{}
	w.failures = nil
}

// Manager keeps one workspace per SSH username. Workspaces are dropped
// when the user's last TUI session ends, like uploads.
type Manager struct {
	mu       sync.Mutex
	users    map[string]*Workspace
	sessions map[string]int
}

// NewManager returns an empty manager.
func NewManager() *Manager {
	return &Manager{users: map[string]*Workspace{}, sessions: map[string]int{}}
}

// For returns the user's workspace, creating it when needed.
func (m *Manager) For(user string) *Workspace {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.users[user]
	if w == nil {
		w = newWorkspace()
		m.users[user] = w
	}
	return w
}

// SessionEnter counts a TUI session for user.
func (m *Manager) SessionEnter(user string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[user]++
}

// SessionLeave drops one TUI session; the last one clears the workspace.
func (m *Manager) SessionLeave(user string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[user] > 0 {
		m.sessions[user]--
	}
	if m.sessions[user] == 0 {
		delete(m.sessions, user)
		if w := m.users[user]; w != nil {
			w.Reset()
		}
		delete(m.users, user)
	}
}

// Clear drops the user's workspace without touching the session count.
func (m *Manager) Clear(user string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.users[user]; w != nil {
		w.Reset()
	}
}

// RootDir is the single top-level directory shared by every path, or "".
// Patches are written relative to it so they apply inside that folder.
func RootDir(paths []string) string {
	root := ""
	for i, p := range paths {
		top, rest, ok := strings.Cut(p, "/")
		if !ok || rest == "" {
			return ""
		}
		if i == 0 {
			root = top
		} else if top != root {
			return ""
		}
	}
	return root
}
