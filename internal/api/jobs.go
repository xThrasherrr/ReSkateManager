package api

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// A job is work that can take longer than a proxy lets one request run, such
// as a backup with mods, a restore or an import (Cloudflare gives up after
// 100 seconds). It runs in the background while the panel polls it.
type job struct {
	jobBase
	st jobStatus
}

// jobBase is what every kind of job has: an id, whose it is, and when it
// ended. mu also guards the status each kind keeps beside it.
type jobBase struct {
	id   string
	user int64

	mu       sync.Mutex
	finished time.Time // zero while it runs
}

func (b *jobBase) base() *jobBase { return b }

// ended is when the job finished, or zero while it runs.
func (b *jobBase) ended() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.finished
}

// jobTable keeps one kind of job by id for the panel to poll, until it has
// been finished for jobKeep.
type jobTable[J interface{ base() *jobBase }] struct {
	mu  sync.Mutex
	all map[string]J
}

// add registers j, unless clashes reports a running job that j must not run
// beside. clashes may be nil.
func (t *jobTable[J]) add(j J, clashes func(running J) bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := time.Now().Add(-jobKeep)
	for id, o := range t.all {
		switch end := o.base().ended(); {
		case end.IsZero():
			if clashes != nil && clashes(o) {
				return false
			}
		case end.Before(cutoff):
			delete(t.all, id)
		}
	}
	if t.all == nil {
		t.all = map[string]J{}
	}
	t.all[j.base().id] = j
	return true
}

// get finds the job the {jid} in the path names, if the caller started it.
func (t *jobTable[J]) get(r *http.Request) (J, bool) {
	t.mu.Lock()
	j, ok := t.all[chi.URLParam(r, "jid")]
	t.mu.Unlock()
	if !ok || j.base().user != from(r).user.ID {
		var none J
		return none, false
	}
	return j, true
}

type jobStatus struct {
	Phase  string `json:"phase"` // running | done | failed
	Done   int64  `json:"done"`  // bytes, out of
	Total  int64  `json:"total"`
	Error  string `json:"error,omitempty"`
	Result any    `json:"result,omitempty"` // what the work answers once done
}

const jobKeep = 15 * time.Minute // a finished job is kept this long for the panel to read

// startJob runs work in the background as the caller's job and answers 202
// with its id. work reports how far it got through the function it's given.
func (a *API) startJob(w http.ResponseWriter, r *http.Request, work func(ctx context.Context, progress func(done, total int64)) (any, error)) {
	j := &job{jobBase: jobBase{id: rand.Text(), user: from(r).user.ID}, st: jobStatus{Phase: "running"}}
	owner := isOwner(r)
	a.jobs.add(j, nil)
	a.goWork(func(base context.Context) {
		ctx, cancel := context.WithTimeout(base, 6*time.Hour)
		defer cancel()
		res, err := func() (res any, err error) {
			defer a.recovered("job", &err)
			return work(ctx, func(done, total int64) {
				j.mu.Lock()
				j.st.Done, j.st.Total = done, total
				j.mu.Unlock()
			})
		}()
		j.mu.Lock()
		defer j.mu.Unlock()
		j.finished = time.Now()
		if err != nil {
			j.st.Phase, j.st.Error = "failed", a.failText(owner, err)
		} else {
			j.st.Phase, j.st.Result = "done", res
		}
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"id": j.id})
}

// goWork runs fn in the background, with a context that ends when the
// manager shuts down; Wait waits for it.
func (a *API) goWork(fn func(ctx context.Context)) {
	base := a.Base
	if base == nil {
		base = context.Background()
	}
	a.working.Add(1)
	a.work.Go(func() {
		defer a.working.Add(-1)
		fn(base)
	})
}

// Busy reports whether background work is running, such as a backup or an
// install that a restart of the manager would cut short.
func (a *API) Busy() bool { return a.working.Load() > 0 }

// Wait waits up to timeout for background work to stop, after Base ended,
// and reports whether it did.
func (a *API) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		a.work.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// recovered turns a panic in background work into an error, so a bug, or a
// zip made to break the manager, fails that one piece of work rather than the
// manager and every server it runs. Defer it, with err pointing at the work's
// error if it has one.
func (a *API) recovered(what string, err *error) {
	p := recover()
	if p == nil {
		return
	}
	a.Log.Error("background work failed unexpectedly", "what", what, "panic", p, "stack", string(debug.Stack()))
	if err != nil {
		*err = errors.New("it failed unexpectedly; the manager log has the details")
	}
}

// jobStatus answers how far one of the caller's jobs got.
func (a *API) jobStatus(w http.ResponseWriter, r *http.Request) {
	j, ok := a.jobs.get(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such job; it may have finished a while ago")
		return
	}
	j.mu.Lock()
	st := j.st
	j.mu.Unlock()
	writeJSON(w, http.StatusOK, st)
}
