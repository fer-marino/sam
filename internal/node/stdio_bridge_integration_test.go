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
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// TestEchoHelperProcess is not a real test: it's a subprocess entry point,
// using the same exec.Command(os.Args[0], "-test.run=...") self-re-exec
// pattern as TestStartRenewalLoop_ExpiredAndFails in node_test.go. It stands
// in for a real command-spawned MCP backend: for every JSON-RPC line with an
// "id" it reads on stdin, it immediately echoes back a minimal success
// response on stdout. This lets the integration tests below drive a real
// StdioBridge end to end, through a real OS process and real pipes, instead
// of the in-memory io.Pipe the other tests in this package use.
func TestEchoHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_ECHO_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	w := bufio.NewWriter(os.Stdout)
	for scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		id, ok := msg["id"]
		if !ok {
			continue // notification: no reply, per JSON-RPC
		}
		out, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		if err != nil {
			continue
		}
		_, _ = w.Write(out)
		_ = w.WriteByte('\n')
		_ = w.Flush()
	}
	os.Exit(0)
}

// newEchoBridge spawns a real TestEchoHelperProcess subprocess and wires a
// StdioBridge to it, the same way createStdioBridgeHandler does for a real
// command: backend.
func newEchoBridge(t *testing.T) *StdioBridge {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestEchoHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_ECHO_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start echo helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	b := &StdioBridge{stdin: stdin, stdout: stdout}
	b.Start()
	return b
}

// TestIntegration_ProbeAndLiveSessionConcurrently reproduces, through the
// real StdioBridge -> bridgeTransport path against a real subprocess (not an
// in-memory pipe), the concurrency scenario documented on Subscribe: a
// periodic health probe (MCPService.Probe in mcp_service.go) opens its own
// short-lived bridgeTransport per check, racing a live session's
// bridgeTransport mid-burst. Before 17da7f6, both shared the same bounded,
// drop-on-full channel also used for SSE, and a burst from one could
// silently cost the other a reply - surfacing downstream as a client-side
// EOF with nothing in between to say why. This test fails on ANY dropped or
// duplicated response, and times out (rather than hanging the suite forever)
// if a fix reintroduces head-of-line blocking between unrelated sessions.
func TestIntegration_ProbeAndLiveSessionConcurrently(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real subprocess; skipped in -short")
	}
	b := newEchoBridge(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// sessionDone stops the probe loop as soon as the live session's bursts
	// finish, independent of ctx's much longer safety bound - otherwise the
	// probe loop (which by design runs until told to stop) and this test's
	// own overall wait would both key off the same ctx expiry, racing each
	// other on a timeout that has nothing to do with correctness.
	sessionDone := make(chan struct{})

	var wg sync.WaitGroup
	errCh := make(chan error, 256)

	// Live session: one long-lived bridgeTransport. Each round fires a burst
	// of calls back-to-back with no interleaved reads - the exact "busy
	// consumer" window the original bug lived in - then drains all replies
	// before starting the next round.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(sessionDone)
		tr := newBridgeTransport(b)
		conn, err := tr.Connect(ctx)
		if err != nil {
			errCh <- fmt.Errorf("session connect: %w", err)
			return
		}
		defer func() { _ = conn.Close() }()

		const rounds = 20
		const burst = 25
		for r := 0; r < rounds; r++ {
			want := make(map[string]bool, burst)
			for i := 0; i < burst; i++ {
				id := fmt.Sprintf("session-%d-%d", r, i)
				want[id] = true
				jid, err := jsonrpc.MakeID(id)
				if err != nil {
					errCh <- fmt.Errorf("session MakeID %s: %w", id, err)
					return
				}
				if err := conn.Write(ctx, &jsonrpc.Request{ID: jid, Method: "tools/call"}); err != nil {
					errCh <- fmt.Errorf("session write %s: %w", id, err)
					return
				}
			}
			for len(want) > 0 {
				msg, err := conn.Read(ctx)
				if err != nil {
					errCh <- fmt.Errorf("session read (round %d, %d still outstanding: %v): %w", r, len(want), keys(want), err)
					return
				}
				resp, ok := msg.(*jsonrpc.Response)
				if !ok {
					continue
				}
				id := fmt.Sprintf("%v", resp.ID.Raw())
				if !want[id] {
					continue // a probe reply multiplexed onto our queue; not ours
				}
				delete(want, id)
			}
		}
	}()

	// Probe: many short-lived bridgeTransports firing continuously for the
	// same duration as the live session's bursts - connect, call, read its
	// own reply (ignoring any of the live session's replies it also sees),
	// close.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; ; i++ {
			select {
			case <-sessionDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			func() {
				tr := newBridgeTransport(b)
				conn, err := tr.Connect(ctx)
				if err != nil {
					errCh <- fmt.Errorf("probe connect %d: %w", i, err)
					return
				}
				defer func() { _ = conn.Close() }()

				id := fmt.Sprintf("probe-%d", i)
				jid, err := jsonrpc.MakeID(id)
				if err != nil {
					errCh <- fmt.Errorf("probe MakeID %s: %w", id, err)
					return
				}
				if err := conn.Write(ctx, &jsonrpc.Request{ID: jid, Method: "initialize"}); err != nil {
					errCh <- fmt.Errorf("probe write %s: %w", id, err)
					return
				}
				for {
					msg, err := conn.Read(ctx)
					if err != nil {
						errCh <- fmt.Errorf("probe read %s: %w", id, err)
						return
					}
					if resp, ok := msg.(*jsonrpc.Response); ok && fmt.Sprintf("%v", resp.ID.Raw()) == id {
						return
					}
				}
			}()
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("test timed out: a dropped reply left a Read() blocked forever (the original bug), or a design change reintroduced head-of-line blocking between the probe and the live session")
	}
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
