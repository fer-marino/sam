// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package node

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// newPipeBridge returns a StdioBridge wired to two in-memory pipes so tests
// can drive stdin/stdout without a real subprocess.
func newPipeBridge() (*StdioBridge, *io.PipeWriter, *bytes.Buffer) {
	stdoutReader, stdoutWriter := io.Pipe()
	stdinBuf := &bytes.Buffer{}
	b := &StdioBridge{
		stdin:  nopWriteCloser{stdinBuf},
		stdout: stdoutReader,
	}
	b.Start()
	return b, stdoutWriter, stdinBuf
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestStdioBridge_SubscribeReceivesLines(t *testing.T) {
	b, stdoutWriter, _ := newPipeBridge()
	defer func() { _ = stdoutWriter.Close() }()

	q, unsub := b.Subscribe()
	defer unsub()

	go func() {
		_, _ = stdoutWriter.Write([]byte("hello\nworld\n"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	want := []string{"hello", "world"}
	for _, w := range want {
		got, ok, err := q.pop(ctx)
		if err != nil {
			t.Fatalf("Subscribe: pop: %v", err)
		}
		if !ok {
			t.Fatalf("Subscribe: queue closed before %q arrived", w)
		}
		if got != w {
			t.Fatalf("Subscribe: got %q, want %q", got, w)
		}
	}
}

// TestStdioBridge_SubscribeDoesNotDropWhileConsumerIsBusy reproduces the
// original bug: a burst of lines arrives while the subscriber isn't yet
// blocked on a receive (e.g. a concurrent health-check probe still holding
// StdioBridge.mu-adjacent work, or simply a goroutine that hasn't reached
// its Read() call yet). The old bounded, drop-on-full channel lost lines in
// exactly this window; the queue must never drop them.
func TestStdioBridge_SubscribeDoesNotDropWhileConsumerIsBusy(t *testing.T) {
	b, stdoutWriter, _ := newPipeBridge()
	defer func() { _ = stdoutWriter.Close() }()

	q, unsub := b.Subscribe()
	defer unsub()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_, _ = fmt.Fprintf(stdoutWriter, "line-%d\n", i)
		}
	}()
	wg.Wait() // all n lines are written (and, on a slow reader, queued) before we ever call pop

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("line-%d", i)
		got, ok, err := q.pop(ctx)
		if err != nil {
			t.Fatalf("pop(%d): %v", i, err)
		}
		if !ok {
			t.Fatalf("pop(%d): queue closed early, want %q", i, want)
		}
		if got != want {
			t.Fatalf("pop(%d): got %q, want %q (a line was dropped or reordered)", i, got, want)
		}
	}
}

// TestStdioBridge_SubscribeCloseUnblocksPop ensures a pop() blocked waiting
// for the next line returns promptly (ok=false) once the bridge's stdout
// closes, rather than hanging forever.
func TestStdioBridge_SubscribeCloseUnblocksPop(t *testing.T) {
	b, stdoutWriter, _ := newPipeBridge()

	q, unsub := b.Subscribe()
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, ok, err := q.pop(ctx)
		if err != nil {
			t.Errorf("pop: %v", err)
		}
		if ok {
			t.Errorf("pop: got ok=true after close, want false")
		}
	}()

	_ = stdoutWriter.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pop did not unblock after stdout closed")
	}
}

func TestStdioBridge_UnsubscribeIsIdempotent(t *testing.T) {
	b, stdoutWriter, _ := newPipeBridge()
	defer func() { _ = stdoutWriter.Close() }()

	_, unsub := b.Subscribe()
	unsub()
	unsub() // must not panic
}

func TestStdioBridge_SendWritesToStdin(t *testing.T) {
	b, stdoutWriter, stdinBuf := newPipeBridge()
	defer func() { _ = stdoutWriter.Close() }()

	if err := b.Send([]byte(`{"jsonrpc":"2.0","id":1}`)); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	got := stdinBuf.String()
	want := `{"jsonrpc":"2.0","id":1}` + "\n"
	if got != want {
		t.Fatalf("Send: stdin got %q, want %q", got, want)
	}
}

// TestSubscriberQueue_PopReleasesBackingArray guards against a real memory
// retention bug flagged in review: re-slicing alone (q.buf = q.buf[1:])
// leaves the popped element's string header live in the backing array,
// keeping its bytes reachable for as long as the array itself is - which,
// for a long-lived session, is every line ever queued. pop must clear the
// popped slot and drop the backing array entirely once drained.
func TestSubscriberQueue_PopReleasesBackingArray(t *testing.T) {
	q := newSubscriberQueue()
	q.push("a")
	q.push("b")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// A second reference into the same backing array, captured before any
	// pop: re-slicing q.buf alone wouldn't touch this array's contents, so
	// backing[0] would still read "a" after popping it if pop didn't
	// explicitly clear the slot.
	q.mu.Lock()
	backing := q.buf[:2:2]
	q.mu.Unlock()

	if got, ok, err := q.pop(ctx); err != nil || !ok || got != "a" {
		t.Fatalf("pop 1: got %q, ok=%v, err=%v", got, ok, err)
	}
	if backing[0] != "" {
		t.Fatalf("pop: backing array slot still holds %q after being popped, want \"\" (string not released)", backing[0])
	}

	if got, ok, err := q.pop(ctx); err != nil || !ok || got != "b" {
		t.Fatalf("pop 2: got %q, ok=%v, err=%v", got, ok, err)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.buf != nil {
		t.Fatalf("pop: buf = %#v after full drain, want nil (backing array not released)", q.buf)
	}
}
