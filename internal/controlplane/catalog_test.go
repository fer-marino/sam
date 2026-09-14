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

package controlplane

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/google/sam/api"
	"github.com/google/sam/internal/identity"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// postCatalog POSTs a raw catalog report body to /nodes/catalog under the
// given biscuit and returns the response status.
func postCatalog(t *testing.T, cpURL string, biscuit []byte, body []byte) int {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, cpURL+"/nodes/catalog", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if biscuit != nil {
		req.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString(biscuit))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestHandleNodeCatalog(t *testing.T) {
	t.Parallel()

	srv, store, cpURL := setupTestServer(t, "")
	defer func() {
		_ = srv.Close()
		_ = store.Close()
	}()

	ctx := context.Background()
	_, biscuitBytes := enrollRefreshTestNode(t, ctx, store)

	body := []byte(`{"services":[{"type":1,"name":"stvv-compliance-docs","description":"doc lookup"},null,{"type":1,"name":"everything","description":"reference server"}]}`)
	if got := postCatalog(t, cpURL, biscuitBytes, body); got != http.StatusNoContent {
		t.Fatalf("HandleNodeCatalog: got status %d, want %d", got, http.StatusNoContent)
	}

	snap := srv.catalogSnapshot()
	if len(snap) != 1 {
		t.Fatalf("expected exactly one reporting peer in the catalog, got %d", len(snap))
	}
	for peerID, entry := range snap {
		if len(entry.Services) != 2 {
			t.Fatalf("expected the null service element to be filtered out, got %d services: %+v", len(entry.Services), entry.Services)
		}
		for i, svc := range entry.Services {
			if svc == nil {
				t.Fatalf("service at index %d is nil, filtering failed", i)
			}
		}
		if entry.ReportedAt.IsZero() {
			t.Errorf("ReportedAt was not set for peer %s", peerID)
		}
	}
}

func TestHandleNodeCatalog_MissingAuth(t *testing.T) {
	t.Parallel()

	srv, store, cpURL := setupTestServer(t, "")
	defer func() {
		_ = srv.Close()
		_ = store.Close()
	}()

	body := []byte(`{"services":[]}`)
	if got := postCatalog(t, cpURL, nil, body); got != http.StatusUnauthorized {
		t.Fatalf("missing Authorization header: got status %d, want %d", got, http.StatusUnauthorized)
	}
}

func TestHandleNodeCatalog_UnenrolledPeer(t *testing.T) {
	t.Parallel()

	srv, store, cpURL := setupTestServer(t, "")
	defer func() {
		_ = srv.Close()
		_ = store.Close()
	}()

	ctx := context.Background()
	cpPriv, _, err := store.GetCurrentKey(ctx)
	if err != nil {
		t.Fatalf("GetCurrentKey: %v", err)
	}

	// A biscuit minted for a peer that was never enrolled (or whose
	// enrollment record has since been removed) must be rejected: a node can
	// only ever report a catalog for itself, and self-reporting requires an
	// admitted enrollment record to attribute the report to.
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	strangerPeer, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("IDFromPrivateKey: %v", err)
	}
	strangerBiscuit, err := identity.MintBootstrapBiscuitToken(
		cpPriv, strangerPeer, api.RoleNode, time.Now().Add(api.BiscuitTokenTTL), nil, nil,
	)
	if err != nil {
		t.Fatalf("MintBootstrapBiscuitToken: %v", err)
	}

	body := []byte(`{"services":[]}`)
	if got := postCatalog(t, cpURL, strangerBiscuit, body); got != http.StatusUnauthorized {
		t.Fatalf("unenrolled peer: got status %d, want %d", got, http.StatusUnauthorized)
	}
}

func TestHandleNodeCatalog_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	srv, store, cpURL := setupTestServer(t, "")
	defer func() {
		_ = srv.Close()
		_ = store.Close()
	}()

	resp, err := http.Get(cpURL + "/nodes/catalog")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /nodes/catalog: got status %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}
