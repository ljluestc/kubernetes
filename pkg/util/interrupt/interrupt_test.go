/*
Copyright The Kubernetes Authors.

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
	"os"
	"strings"
	"testing"
)

// TestCloseRunsNotify verifies that normal completion invokes all notify functions.
func TestCloseRunsNotify(t *testing.T) {
	var calls []string
	h := New(func(os.Signal) { calls = append(calls, "final") },
		func() { calls = append(calls, "a") },
		func() { calls = append(calls, "b") },
	)
	if err := h.Run(func() error { return nil }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 2 || calls[0] != "a" || calls[1] != "b" {
		t.Errorf("expected [a b], got %v", calls)
	}
}

// TestCloseIsIdempotent verifies that notify functions run exactly once even if Close
// is called multiple times.
func TestCloseIsIdempotent(t *testing.T) {
	count := 0
	h := New(nil, func() { count++ })
	h.Close()
	h.Close()
	if count != 1 {
		t.Errorf("expected notify called once, got %d", count)
	}
}

// TestSignalCallsFinalAfterClose is the core regression test for the bug described in
// https://github.com/kubernetes/kubernetes/issues/81098.
//
// When fn() returns at the same moment a signal arrives, Close() may win the once race and
// consume it before Signal() gets to run. In the old code, final was inside once.Do, so it
// was never called in that scenario. The fix moves final outside once.Do in Signal().
func TestSignalCallsFinalAfterClose(t *testing.T) {
	notifyCalls := 0
	finalCalled := false

	h := New(
		func(s os.Signal) { finalCalled = true },
		func() { notifyCalls++ },
	)

	// Simulate Close() winning the once race before Signal() runs.
	h.Close()

	// Signal() must still call final, even though the once is exhausted.
	h.Signal(os.Interrupt)

	if notifyCalls != 1 {
		t.Errorf("expected notify called once, got %d", notifyCalls)
	}
	if !finalCalled {
		t.Error("final was not called; process would have silently survived the signal")
	}
}

// TestSignalCallsFinalWithNilFinalExits is intentionally not tested (it calls os.Exit).
// Coverage for the nil-final path is provided by integration tests.

// TestRunReturnErrorWhenAlreadyRunning verifies that a second concurrent (or serial) Run
// returns a descriptive error instead of silently doing nothing.
func TestRunReturnErrorWhenAlreadyRunning(t *testing.T) {
	h := New(func(os.Signal) {}, func() {})

	// First run completes successfully.
	if err := h.Run(func() error { return nil }); err != nil {
		t.Fatalf("first Run: unexpected error: %v", err)
	}

	// Second run on the same handler must fail.
	err := h.Run(func() error { return nil })
	if err == nil {
		t.Fatal("second Run: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("second Run: unexpected error message: %v", err)
	}
}

// TestChainCleanPath verifies that when a chained handler completes without a signal,
// all notify functions (chain-local + parent) are invoked and final is NOT called.
func TestChainCleanPath(t *testing.T) {
	var calls []string
	h1 := New(
		func(os.Signal) { calls = append(calls, "final") },
		func() { calls = append(calls, "foo") },
		func() { calls = append(calls, "bar") },
	)
	h2 := Chain(h1, func() { calls = append(calls, "qux") })

	if err := h2.Run(func() error { return nil }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect qux (chain-local), then foo, bar (parent notify). final must NOT appear.
	want := []string{"qux", "foo", "bar"}
	if len(calls) != len(want) {
		t.Fatalf("expected %v, got %v", want, calls)
	}
	for i, c := range calls {
		if c != want[i] {
			t.Errorf("calls[%d]: want %q, got %q", i, want[i], c)
		}
	}
}

// TestChainSignalPath verifies that when a chained handler receives a signal, all notify
// functions run and final IS called — the bug thockin identified where myfinal was never
// called in the chain+signal scenario.
func TestChainSignalPath(t *testing.T) {
	var calls []string
	h1 := New(
		func(s os.Signal) { calls = append(calls, "final") },
		func() { calls = append(calls, "foo") },
		func() { calls = append(calls, "bar") },
	)
	h2 := Chain(h1, func() { calls = append(calls, "qux") })

	// Simulate the signal arriving before fn() returns by firing Signal() directly.
	// (Driving a real OS signal in a unit test is fragile and process-wide.)
	h2.Signal(os.Interrupt)

	// Expect qux, foo, bar (all notify), then final.
	want := []string{"qux", "foo", "bar", "final"}
	if len(calls) != len(want) {
		t.Fatalf("expected %v, got %v", want, calls)
	}
	for i, c := range calls {
		if c != want[i] {
			t.Errorf("calls[%d]: want %q, got %q", i, want[i], c)
		}
	}
}

// TestChainIsIndependentFromParent verifies that running h2 (a chain of h1) does not
// exhaust h1's once, so h1 can still be Run independently afterwards.
func TestChainIsIndependentFromParent(t *testing.T) {
	parentNotify := 0
	h1 := New(func(os.Signal) {}, func() { parentNotify++ })
	h2 := Chain(h1, func() {})

	// Running h2 should invoke h1's notify (copied into h2), but not h1's own once.
	if err := h2.Run(func() error { return nil }); err != nil {
		t.Fatalf("h2.Run: unexpected error: %v", err)
	}

	// h1 hasn't been Run yet, so its own once is still fresh.
	if err := h1.Run(func() error { return nil }); err != nil {
		t.Fatalf("h1.Run: unexpected error: %v", err)
	}

	// parentNotify should be 2: once from h2 (copy), once from h1 itself.
	if parentNotify != 2 {
		t.Errorf("expected parentNotify==2, got %d", parentNotify)
	}
}
