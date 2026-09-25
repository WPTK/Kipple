package backup

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Job states.
const (
	JobBuilding = "building"
	JobReady    = "ready"
	JobFailed   = "failed"
)

// ErrNoJob: unknown job, or a finished one whose download link is gone.
var ErrNoJob = errors.New("backup: no such export job")

// maxJobs bounds the finished jobs remembered for polling.
const maxJobs = 8

// Job is one export running (or finished) in the background. Its context is the
// Manager's, not a request's: a client that disconnects or is cut off by a proxy
// timeout does not cancel the build.
type Job struct {
	ID string

	mu       sync.Mutex
	status   string
	exp      Export
	err      error
	done     chan struct{}
	finished time.Time // guarded by Manager.mu
}

// Done is closed when the job is ready or has failed.
func (j *Job) Done() <-chan struct{} { return j.done }

// JobState is a snapshot of a job.
type JobState struct {
	Status string
	Export Export // set when Status is JobReady
	Err    error  // set when Status is JobFailed
}

// State reads the job's current state.
func (j *Job) State() JobState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JobState{Status: j.status, Export: j.exp, Err: j.err}
}

// Start begins an export in the background and returns at once. ErrBusy when a
// snapshot or another export holds the slot; everything else (free space, size,
// a failed check) is reported through the job. The token's TTL counts from the
// moment the export is ready.
func (m *Manager) Start() (*Job, error) {
	release, err := m.o.DB.TrySnapshot()
	if err != nil {
		return nil, ErrBusy
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		release()
		return nil, err
	}
	j := &Job{ID: hex.EncodeToString(b), status: JobBuilding, done: make(chan struct{})}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		release()
		return nil, ErrBusy
	}
	m.pruneJobsLocked()
	m.jobs = append(m.jobs, j)
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		exp, err := m.build(m.base, release)
		j.mu.Lock()
		if err != nil {
			j.status, j.err = JobFailed, err
			m.log.Warn("backup: export job failed", "job", j.ID, "err", err)
		} else {
			j.status, j.exp = JobReady, exp
		}
		j.mu.Unlock()
		close(j.done)
		m.mu.Lock()
		j.finished = m.o.Now()
		m.mu.Unlock()
	}()
	return j, nil
}

// pruneJobsLocked forgets old finished jobs, keeping at most maxJobs.
func (m *Manager) pruneJobsLocked() {
	keep := m.jobs[:0]
	for _, j := range m.jobs {
		select {
		case <-j.done:
			if !j.finished.IsZero() && m.o.Now().Sub(j.finished) > time.Hour {
				continue
			}
		default:
		}
		keep = append(keep, j)
	}
	m.jobs = keep
	if len(m.jobs) >= maxJobs {
		m.jobs = append([]*Job(nil), m.jobs[len(m.jobs)-maxJobs+1:]...)
	}
}

// Job looks up a job by id. A finished job whose export has been downloaded,
// replaced or expired is gone (ErrNoJob), as an unknown id is.
func (m *Manager) Job(id string) (JobState, error) {
	m.mu.Lock()
	var j *Job
	for _, c := range m.jobs {
		if c.ID == id {
			j = c
		}
	}
	m.mu.Unlock()
	if j == nil {
		return JobState{}, ErrNoJob
	}
	st := j.State()
	if st.Status == JobReady && !m.tokenLive(st.Export.Token) {
		return JobState{}, ErrNoJob
	}
	return st, nil
}

func (m *Manager) tokenLive(token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur != nil && m.cur.token == token && m.o.Now().Before(m.cur.expires)
}
