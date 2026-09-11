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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/google/sam/api"
)

type StdioBridge struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	mu      sync.Mutex
	clients map[chan string]bool
	queues  map[*subscriberQueue]bool
	calls   map[string]chan string
}

func (b *StdioBridge) Start() {
	b.clients = make(map[chan string]bool)
	b.queues = make(map[*subscriberQueue]bool)
	b.calls = make(map[string]chan string)
	go func() {
		scanner := bufio.NewScanner(b.stdout)
		for scanner.Scan() {
			line := scanner.Text()

			b.mu.Lock()
			if len(line) > 0 && line[0] == '{' {
				var msg map[string]any
				if err := json.Unmarshal([]byte(line), &msg); err == nil {
					if idVal, ok := msg["id"]; ok {
						reqIDStr := fmt.Sprintf("%v", idVal)
						if ch, found := b.calls[reqIDStr]; found {
							select {
							case ch <- line:
							default:
							}
						}
					}
				}
			}

			for ch := range b.clients {
				select {
				case ch <- line:
				default:
				}
			}
			for q := range b.queues {
				q.push(line)
			}
			b.mu.Unlock()
		}

		b.mu.Lock()
		for ch := range b.clients {
			close(ch)
			delete(b.clients, ch)
		}
		for q := range b.queues {
			q.closeQueue()
		}
		b.queues = make(map[*subscriberQueue]bool)
		for _, ch := range b.calls {
			close(ch)
		}
		b.calls = make(map[string]chan string)
		b.mu.Unlock()
	}()
}

func (b *StdioBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
			return
		}

		ch := make(chan string, 10)
		b.mu.Lock()
		b.clients[ch] = true
		b.mu.Unlock()

		// Flush headers immediately to establish the stream
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		defer func() {
			b.mu.Lock()
			delete(b.clients, ch)
			b.mu.Unlock()
			close(ch)
		}()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case line, ok := <-ch:
				if !ok {
					return
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", line); err != nil {
					logger.Errorf("Failed to write to SSE client: %v", err)
					return
				}
				flusher.Flush()
			}
		}
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		defer func() { _ = r.Body.Close() }()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusInternalServerError)
			return
		}

		var msg map[string]any
		isCall := false
		var reqID any
		if err := json.Unmarshal(body, &msg); err == nil {
			if id, ok := msg["id"]; ok {
				isCall = true
				reqID = id
			}
		}

		var ch <-chan string
		var unsub func()
		if isCall {
			reqIDStr := fmt.Sprintf("%v", reqID)
			callCh := make(chan string, 1)
			b.mu.Lock()
			b.calls[reqIDStr] = callCh
			b.mu.Unlock()
			ch = callCh
			unsub = func() {
				b.mu.Lock()
				if existing, ok := b.calls[reqIDStr]; ok && existing == callCh {
					delete(b.calls, reqIDStr)
					close(callCh)
				}
				b.mu.Unlock()
			}
			defer unsub()
		}

		b.mu.Lock()
		_, err = b.stdin.Write(append(body, '\n'))
		b.mu.Unlock()

		if err != nil {
			http.Error(w, "Failed to write to process stdin", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Mcp-Session-Id", "stdio-bridge")

		if !isCall {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		ctx := r.Context()
		select {
		case <-ctx.Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(line))
			return
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// Subscribe registers a new subscriber queue for stdout lines and returns it
// along with an idempotent unsubscribe function.
//
// Unlike the plain chan string clients map above (bounded cap 10, drops on a
// slow consumer - fine for the ServeHTTP SSE feed, where a browser tab
// missing a line just misses a live update), Subscribe's only caller is
// bridgeTransport, i.e. an MCP request/response session: every line matters,
// because the one dropped could be the exact reply a Read() call is blocked
// on, with nothing left to ever wake it. bridgeTransport used to share the
// same bounded, drop-on-full channel as SSE; a periodic health probe and a
// live tools/call session subscribing to the same backend concurrently was
// enough to lose a message that way, surfacing as a client-side EOF even
// though the backend had answered correctly. Every subscriber here instead
// gets its own unbounded, order-preserving queue.
func (b *StdioBridge) Subscribe() (*subscriberQueue, func()) {
	q := newSubscriberQueue()
	b.mu.Lock()
	b.queues[q] = true
	b.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.queues, q)
			b.mu.Unlock()
			q.closeQueue()
		})
	}
	return q, unsub
}

// subscriberQueue is an unbounded, order-preserving mailbox for one
// bridgeTransport session's stdout lines.
//
// push is called from the bridge's single stdout-scanning goroutine (with
// StdioBridge.mu held) and must never block or drop a line. pop is called by
// the session's own reader; it blocks until a line is queued, the bridge
// closes the queue, or ctx is done.
type subscriberQueue struct {
	mu     sync.Mutex
	buf    []string
	closed bool
	notify chan struct{} // capacity 1: a "there may be new work" flag, coalesced
}

func newSubscriberQueue() *subscriberQueue {
	return &subscriberQueue{notify: make(chan struct{}, 1)}
}

func (q *subscriberQueue) push(line string) {
	q.mu.Lock()
	q.buf = append(q.buf, line)
	q.mu.Unlock()
	q.wake()
}

func (q *subscriberQueue) closeQueue() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.wake()
}

func (q *subscriberQueue) wake() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// pop returns the next queued line in FIFO order. ok is false only once the
// queue has been closed and fully drained.
func (q *subscriberQueue) pop(ctx context.Context) (line string, ok bool, err error) {
	for {
		q.mu.Lock()
		if len(q.buf) > 0 {
			line = q.buf[0]
			// Re-slicing alone leaves the popped element's string header
			// live in the backing array, keeping its bytes reachable for
			// as long as the array itself is - which, for a long-lived
			// session, is every line ever queued. Clear the slot before
			// advancing, and drop the backing array entirely once drained,
			// rather than let it sit at its peak size for the queue's
			// remaining lifetime.
			q.buf[0] = ""
			q.buf = q.buf[1:]
			if len(q.buf) == 0 {
				q.buf = nil
			}
			q.mu.Unlock()
			return line, true, nil
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return "", false, nil
		}
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-q.notify:
		}
	}
}

// Send writes data to the child's stdin, appending a newline.
func (b *StdioBridge) Send(data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err := b.stdin.Write(append(data, '\n'))
	return err
}

func createStdioBridgeHandler(cmdBackend *api.CommandBackend) (http.Handler, *exec.Cmd, error) {
	cmd := exec.Command(cmdBackend.Command[0], cmdBackend.Command[1:]...)
	cmd.Env = os.Environ()
	for k, v := range cmdBackend.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	bridge := &StdioBridge{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
	}
	bridge.Start()

	return bridge, cmd, nil
}
