// Package pull runs model downloads into Docker Model Runner in the
// background. One Manager is shared by all sessions, so a pull keeps going
// when the user who started it disconnects, and everyone sees the queue.
package pull

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
)

// Puller downloads one model. *dmr.Client implements it.
type Puller interface {
	Pull(ctx context.Context, ref string, fn func(dmr.PullProgress)) error
}

// State is where a job is.
type State int

const (
	Queued State = iota
	Running
	Done
	Failed
	Cancelled
)

func (s State) String() string {
	switch s {
	case Queued:
		return "queued"
	case Running:
		return "pulling"
	case Done:
		return "done"
	case Failed:
		return "failed"
	default:
		return "cancelled"
	}
}

// Finished reports whether the job no longer runs.
func (s State) Finished() bool { return s >= Done }

// ErrQueued is returned when the same model is already queued or pulling.
var ErrQueued = errors.New("this model is already queued or downloading")

// Job is a snapshot of one download.
type Job struct {
	ID       int
	Ref      string
	User     string
	Size     int64 // expected bytes, from the Hub
	State    State
	Done     int64
	Total    int64
	Rate     float64 // bytes per second, smoothed
	Message  string
	Err      string
	Queued   time.Time
	Started  time.Time
	Finished time.Time
}

// Expected is the best known size: DMR's total once it is known.
func (j Job) Expected() int64 {
	if j.Total > j.Size {
		return j.Total
	}
	return j.Size
}

// Remaining is the bytes still to be written.
func (j Job) Remaining() int64 {
	if j.State.Finished() {
		return 0
	}
	r := j.Expected() - j.Done
	if r < 0 {
		return 0
	}
	return r
}

// Fraction is the progress from 0 to 1, or -1 when the size is unknown.
func (j Job) Fraction() float64 {
	if j.State == Done {
		return 1
	}
	e := j.Expected()
	if e <= 0 {
		return -1
	}
	f := float64(j.Done) / float64(e)
	if f > 1 {
		f = 1
	}
	return f
}

type job struct {
	Job
	ctx      context.Context
	cancel   context.CancelFunc
	lastAt   time.Time
	lastDone int64
}

// Manager queues and runs pulls.
type Manager struct {
	ctx    context.Context
	puller Puller
	limit  int
	keep   time.Duration
	now    func() time.Time

	mu       sync.Mutex
	jobs     []*job
	nextID   int
	finished int
	onDone   []func(Job)
}

// New starts a manager. ctx ends all pulls when the process stops.
// concurrency is how many pulls run at once (DMR itself allows two).
func New(ctx context.Context, p Puller, concurrency int) *Manager {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Manager{ctx: ctx, puller: p, limit: concurrency, keep: 15 * time.Minute, now: time.Now}
}

// OnFinish registers a callback that runs after every finished job.
func (m *Manager) OnFinish(fn func(Job)) {
	m.mu.Lock()
	m.onDone = append(m.onDone, fn)
	m.mu.Unlock()
}

// Enqueue adds a pull. size is the expected download size in bytes.
func (m *Manager) Enqueue(user, ref string, size int64) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.Ref == ref && !j.State.Finished() {
			return j.Job, ErrQueued
		}
	}
	m.nextID++
	ctx, cancel := context.WithCancel(m.ctx)
	j := &job{Job: Job{ID: m.nextID, Ref: ref, User: user, Size: size, State: Queued, Queued: m.now()}, ctx: ctx, cancel: cancel}
	m.jobs = append(m.jobs, j)
	m.dispatch()
	return j.Job, nil
}

// dispatch starts queued jobs in order while slots are free. m.mu is held.
func (m *Manager) dispatch() {
	running := 0
	for _, j := range m.jobs {
		if j.State == Running {
			running++
		}
	}
	for _, j := range m.jobs {
		if running >= m.limit {
			return
		}
		if j.State != Queued {
			continue
		}
		j.State = Running
		j.Started = m.now()
		j.lastAt = j.Started
		running++
		go m.run(j)
	}
}

func (m *Manager) run(j *job) {
	ctx := j.ctx
	err := m.puller.Pull(ctx, j.Ref, func(p dmr.PullProgress) {
		m.mu.Lock()
		defer m.mu.Unlock()
		now := m.now()
		if dt := now.Sub(j.lastAt).Seconds(); dt >= 0.5 {
			inst := float64(p.Done-j.lastDone) / dt
			if inst < 0 {
				inst = 0
			}
			if j.Rate == 0 {
				j.Rate = inst
			} else {
				j.Rate = 0.7*j.Rate + 0.3*inst
			}
			j.lastAt, j.lastDone = now, p.Done
		}
		j.Done, j.Total = p.Done, p.Total
		if p.Message != "" {
			j.Message = p.Message
		}
	})
	m.finish(j, err)
}

func (m *Manager) finish(j *job, err error) {
	m.mu.Lock()
	if j.State.Finished() {
		m.mu.Unlock()
		return
	}
	switch {
	case err == nil:
		j.State = Done
		if j.Total > 0 {
			j.Done = j.Total
		}
	case errors.Is(err, context.Canceled):
		j.State = Cancelled
	default:
		j.State = Failed
		j.Err = err.Error()
	}
	j.Finished = m.now()
	j.cancel()
	m.finished++
	m.dispatch()
	snap := j.Job
	fns := append([]func(Job){}, m.onDone...)
	m.mu.Unlock()
	for _, fn := range fns {
		fn(snap)
	}
}

// Cancel stops a queued or running job. DMR stops the download when the
// request is cancelled.
func (m *Manager) Cancel(id int) bool {
	m.mu.Lock()
	var target *job
	for _, j := range m.jobs {
		if j.ID == id && !j.State.Finished() {
			target = j
		}
	}
	m.mu.Unlock()
	if target == nil {
		return false
	}
	m.mu.Lock()
	queued := target.State == Queued
	m.mu.Unlock()
	target.cancel()
	if queued {
		m.finish(target, context.Canceled)
	}
	return true
}

// Jobs is a snapshot, oldest first. Finished jobs stay listed for a while.
func (m *Manager) Jobs() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	kept := m.jobs[:0]
	for _, j := range m.jobs {
		if j.State.Finished() && now.Sub(j.Finished) > m.keep {
			continue
		}
		kept = append(kept, j)
	}
	m.jobs = kept
	out := make([]Job, len(kept))
	for i, j := range kept {
		out[i] = j.Job
	}
	return out
}

// Pending is the bytes that queued and running pulls still need.
func (m *Manager) Pending() int64 {
	var n int64
	for _, j := range m.Jobs() {
		n += j.Remaining()
	}
	return n
}

// Finished counts finished jobs, so a screen can tell when to refresh.
func (m *Manager) Finished() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.finished
}

// Space is the outcome of the free-space check before a pull.
type Space struct {
	Known   bool  // the model disk could be read
	Free    int64 // bytes available on the model disk
	Size    int64 // download size of this model
	Pending int64 // bytes other queued pulls still need
	Reserve int64 // bytes to keep free
}

// Need is what has to be free for the pull to be allowed.
func (s Space) Need() int64 { return s.Size + s.Pending + s.Reserve }

// OK reports whether there is room. An unknown disk is not refused here;
// the caller decides.
func (s Space) OK() bool { return !s.Known || s.Free >= s.Need() }
