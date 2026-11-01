package engine

import (
	"context"
	"sync"
)

// checkRun is one check of the Changed Actions, in flight until done is
// closed, and its report R once it has one. Whoever asks while it runs
// waits for it too, and hears its progress P.
type checkRun[R, P any] struct {
	sig  string
	done chan struct{}

	mu        sync.Mutex
	report    *R
	err       error
	last      *P
	listeners map[int]func(P)
	nextID    int
}

func newCheckRun[R, P any](sig string) *checkRun[R, P] {
	return &checkRun[R, P]{sig: sig, done: make(chan struct{}), listeners: map[int]func(P){}}
}

func (r *checkRun[R, P]) running() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

// publish tells everyone waiting how far the check got.
func (r *checkRun[R, P]) publish(p P) {
	r.mu.Lock()
	r.last = &p
	listeners := make([]func(P), 0, len(r.listeners))
	for _, l := range r.listeners {
		listeners = append(listeners, l)
	}
	r.mu.Unlock()
	for _, l := range listeners {
		l(p)
	}
}

// wait waits for the report, telling progress how far the check got: where
// it is now, then every step. ctx bounds only this caller's wait.
func (r *checkRun[R, P]) wait(ctx context.Context, progress func(P)) (*R, error) {
	if progress != nil {
		r.mu.Lock()
		id := r.nextID
		r.nextID++
		r.listeners[id] = progress
		if r.last != nil && r.report == nil && r.err == nil {
			// Under the lock, so it can't arrive after a newer step.
			progress(*r.last)
		}
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			delete(r.listeners, id)
			r.mu.Unlock()
		}()
	}
	select {
	case <-r.done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.report, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
