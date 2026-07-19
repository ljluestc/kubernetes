/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package interrupt

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// terminationSignals are signals that cause the program to exit in the
// supported platforms (linux, darwin, windows).
var terminationSignals = []os.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT}

// Handler guarantees execution of notifications after a critical section (the function passed
// to a Run method), even in the presence of process termination. It guarantees exactly once
// invocation of the provided notify functions.
type Handler struct {
	notify []func()
	final  func(os.Signal)
	once   sync.Once

	// mu guards running; a Handler may only be used for one critical section.
	mu      sync.Mutex
	running bool
}

// New creates a new handler that guarantees all notify functions are run after the critical
// section exits (or is interrupted by the OS), then invokes the final handler. If no final
// handler is specified, the default final is `os.Exit(1)`. A handler can only be used for
// one critical section.
func New(final func(os.Signal), notify ...func()) *Handler {
	return &Handler{
		final:  final,
		notify: notify,
	}
}

// Chain creates a new Handler that prepends additional notify functions before the parent
// handler's notify functions and inherits the parent's final handler. The returned handler
// is fully independent: it has its own sync.Once and running guard, so calling Run on it
// does not interfere with the parent.
//
// Chain must not be called on a parent handler that is currently being Run; doing so
// produces undefined behaviour because the parent's once may already be exhausted.
func Chain(parent *Handler, notify ...func()) *Handler {
	combined := make([]func(), 0, len(notify)+len(parent.notify))
	combined = append(combined, notify...)
	combined = append(combined, parent.notify...)
	return &Handler{
		final:  parent.final,
		notify: combined,
	}
}

// Close executes all the notification handlers if they have not yet been executed.
func (h *Handler) Close() {
	h.once.Do(func() {
		for _, fn := range h.notify {
			fn()
		}
	})
}

// Signal is called when an os.Signal is received. It guarantees that all notify functions
// are executed (exactly once, coordinated with Close), then unconditionally invokes the
// final handler.
//
// Decoupling final from once.Do is intentional: if fn() returns at the same moment a
// signal arrives, Close() may win the once race and consume it before Signal() runs.
// Without this separation, final would never be called and the process would silently
// survive a termination signal.
func (h *Handler) Signal(s os.Signal) {
	h.once.Do(func() {
		for _, fn := range h.notify {
			fn()
		}
	})
	// Always invoke final on the signal path, even if Close() already fired the once.
	if h.final == nil {
		os.Exit(1)
	}
	h.final(s)
}

// Run ensures that any notifications are invoked after the provided fn exits (even if the
// process is interrupted by an OS termination signal). Notifications are only invoked once
// per Handler instance. Calling Run on a handler that is already running, or that has
// already completed a run, returns an error immediately.
func (h *Handler) Run(fn func() error) error {
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		return fmt.Errorf("interrupt: handler already in use; each Handler may only run one critical section")
	}
	h.running = true
	h.mu.Unlock()

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, terminationSignals...)
	defer func() {
		signal.Stop(ch)
		close(ch)
	}()
	go func() {
		sig, ok := <-ch
		if !ok {
			return
		}
		h.Signal(sig)
	}()
	defer h.Close()
	return fn()
}
