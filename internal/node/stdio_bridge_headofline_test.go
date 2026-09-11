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
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// TestIntegration_StuckSubscriberDoesNotStallOthers guards the other half of
// Subscribe's contract: never dropping a line must not come at the cost of
// one subscriber's pace affecting another's. It connects a bridgeTransport
// and never reads its replies - a realistic stand-in for a session whose own
// downstream consumer has stalled, exactly the asymmetric-proxy scenario
// aojea flagged on #375 ("closing asymmetric streams on proxies is hard") -
// then asserts a second, healthy bridgeTransport on the same backend keeps
// getting answered promptly regardless. A fix for the drop bug that makes
// push block on a full per-subscriber buffer would pass every drop test and
// still fail this one: the scan loop dispatches to every subscriber under
// the same lock, so blocking on one wedges delivery - and, since Send also
// takes that lock, new requests too - for all of them.
func TestIntegration_StuckSubscriberDoesNotStallOthers(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real subprocess; skipped in -short")
	}
	b := newEchoBridge(t)

	stuckTr := newBridgeTransport(b)
	stuckConn, err := stuckTr.Connect(context.Background())
	if err != nil {
		t.Fatalf("stuck subscriber connect: %v", err)
	}
	defer func() { _ = stuckConn.Close() }()

	// Send enough calls to the stuck subscriber's own queue to overflow any
	// reasonable fixed buffer, without ever reading a reply.
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("stuck-%d", i)
		jid, _ := jsonrpc.MakeID(id)
		writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := stuckConn.Write(writeCtx, &jsonrpc.Request{ID: jid, Method: "tools/call"})
		cancel()
		if err != nil {
			t.Logf("stuck subscriber: write %d stopped early (%v) - fine, the point is only to fill its queue", i, err)
			break
		}
	}

	// The healthy subscriber must complete quickly regardless of the above.
	healthyTr := newBridgeTransport(b)
	healthyConn, err := healthyTr.Connect(context.Background())
	if err != nil {
		t.Fatalf("healthy subscriber connect: %v", err)
	}
	defer func() { _ = healthyConn.Close() }()

	const budget = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	id := "healthy-1"
	jid, _ := jsonrpc.MakeID(id)
	if err := healthyConn.Write(ctx, &jsonrpc.Request{ID: jid, Method: "tools/call"}); err != nil {
		t.Fatalf("healthy subscriber write: %v", err)
	}
	for {
		msg, err := healthyConn.Read(ctx)
		if err != nil {
			t.Fatalf("healthy subscriber did not get its reply within %s while another subscriber was stuck not reading: %v (head-of-line blocking: one wedged consumer stalling every other subscriber of the same backend)", budget, err)
		}
		if resp, ok := msg.(*jsonrpc.Response); ok && fmt.Sprintf("%v", resp.ID.Raw()) == id {
			return
		}
	}
}
