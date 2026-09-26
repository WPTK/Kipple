package imgproxy

import (
	"net/http"
	"sync"
)

// fillState is where a cache fill stands.
type fillState int

const (
	fillRunning fillState = iota
	fillDone              // the body is complete in the file (committed or not)
	fillFailed            // the source failed, the body was cut or over the cap
	fillHandoff           // the cache write failed: the client loop reads the rest of the body itself
)

// progress is the fill goroutine telling the client loop how far the cache
// file has been written.
type progress struct {
	mu        sync.Mutex
	cond      *sync.Cond
	n         int64
	st        fillState
	commit    bool
	pending   []byte // handoff: bytes read from the source but not written to the file
	finished  chan struct{}
	closeOnce sync.Once
}

func newProgress() *progress {
	p := &progress{finished: make(chan struct{})}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *progress) add(n int64) {
	p.mu.Lock()
	p.n += n
	p.mu.Unlock()
	p.cond.Broadcast()
}

func (p *progress) finish(st fillState, committed bool) {
	p.mu.Lock()
	p.st, p.commit = st, committed
	p.mu.Unlock()
	p.cond.Broadcast()
	p.closeOnce.Do(func() { close(p.finished) })
}

func (p *progress) handoff(pending []byte) {
	p.mu.Lock()
	p.st, p.pending = fillHandoff, append([]byte(nil), pending...)
	p.mu.Unlock()
	p.cond.Broadcast()
	p.closeOnce.Do(func() { close(p.finished) })
}

// next blocks until more than off bytes are in the file or the fill has ended.
func (p *progress) next(off int64) (n int64, st fillState, pending []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.n <= off && p.st == fillRunning {
		p.cond.Wait()
	}
	return p.n, p.st, p.pending
}

// wait blocks until the fill has ended.
func (p *progress) wait() fillState {
	<-p.finished
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}

func (p *progress) committed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.commit
}

// probe is the ResponseWriter the thumbnail path runs the ordinary original
// path through (ensureOriginal). It records the response instead of sending
// it, because the client is to get the thumbnail, not the original. When the
// path finds it cannot cache the original (low disk, a failed commit) it
// switches the probe to forward, and the original it is fetching anyway goes
// to the client: the source is contacted once, not twice.
type probe struct {
	real http.ResponseWriter
	h    http.Header
	code int
	fwd  bool
}

func (p *probe) Header() http.Header {
	if p.fwd {
		return p.real.Header()
	}
	if p.h == nil {
		p.h = http.Header{}
	}
	return p.h
}

func (p *probe) WriteHeader(code int) {
	if p.fwd {
		p.real.WriteHeader(code)
		return
	}
	if p.code == 0 {
		p.code = code
	}
}

func (p *probe) Write(b []byte) (int, error) {
	if p.fwd {
		return p.real.Write(b)
	}
	if p.code == 0 {
		p.code = http.StatusOK
	}
	return len(b), nil
}

// forward sends everything from now on to the client, replaying the headers
// set so far and a status already written.
func (p *probe) forward() {
	if p.fwd {
		return
	}
	p.fwd = true
	p.replay()
}

// replay copies the recorded headers and status to the client.
func (p *probe) replay() {
	dst := p.real.Header()
	for k, v := range p.h {
		dst[k] = v
	}
	if p.code != 0 {
		p.real.WriteHeader(p.code)
	}
}
