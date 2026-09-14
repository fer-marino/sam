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
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/sam/api"
	"github.com/google/sam/internal/identity"
)

// nodeCatalogEntry is what HandleNodeCatalog caches per reporting peer.
type nodeCatalogEntry struct {
	Services   []*api.ServiceInfo `json:"services"`
	ReportedAt time.Time          `json:"reported_at"`
}

// nodeCatalogRequest is HandleNodeCatalog's request body. The reporting
// peer's identity comes from its verified Biscuit, never from this body -
// a node can only ever report on itself.
type nodeCatalogRequest struct {
	Services []*api.ServiceInfo `json:"services"`
}

// catalogSnapshot returns a stable copy of the current node service catalog
// cache, safe to range over or marshal without holding catalogMu.
func (s *Server) catalogSnapshot() map[string]nodeCatalogEntry {
	s.catalogMu.RLock()
	defer s.catalogMu.RUnlock()
	snap := make(map[string]nodeCatalogEntry, len(s.catalog))
	for k, v := range s.catalog {
		snap[k] = v
	}
	return snap
}

// HandleNodeCatalog HTTP POST /nodes/catalog - a node self-reports the
// services it currently has registered locally (the same data
// list_local_services already answers on the node itself), so the control
// plane can show mesh-wide service topology without needing to be a DHT
// participant or open a P2P connection to every enrolled node itself.
//
// This is a live-status cache, not authoritative state: a node that goes
// offline without ever reporting an empty catalog just leaves its last
// report in place until ReportedAt visibly goes stale. Good enough for an
// admin-facing "what's running where" view; not a substitute for the real
// per-request Biscuit authorization every actual service call still goes
// through independently.
func (s *Server) HandleNodeCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, "Missing node Biscuit token in Authorization header", http.StatusUnauthorized)
		return
	}
	biscuitBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Bearer "))
	if err != nil {
		http.Error(w, "Malformed base64 token", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	validKeys, err := s.store.GetAllValidKeys(ctx)
	if err != nil {
		logger.Errorf("Failed to retrieve valid signing keys: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	var trustedKeys []ed25519.PublicKey
	for _, k := range validKeys {
		trustedKeys = append(trustedKeys, k.Public)
	}

	peerID, err := identity.VerifyAndExtractPeerID(trustedKeys, biscuitBytes, s.config.BiscuitTimeout)
	if err != nil {
		logger.Warnw("Invalid biscuit presented to /nodes/catalog", "error", err)
		http.Error(w, "Invalid biscuit: "+err.Error(), http.StatusUnauthorized)
		return
	}

	nodeRecord, err := s.store.GetNode(ctx, peerID.String())
	if err != nil || nodeRecord == nil || nodeRecord.CheckAdmission(time.Now()) != nil {
		http.Error(w, "Node not enrolled or not admitted", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req nodeCatalogRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	// A malformed report (e.g. {"services": [null]}) unmarshals into a nil
	// element rather than failing - filter those out so a bad report from one
	// node can't crash rendering for every node's entry in the console.
	var validServices []*api.ServiceInfo
	for _, svc := range req.Services {
		if svc != nil {
			validServices = append(validServices, svc)
		}
	}

	s.catalogMu.Lock()
	s.catalog[peerID.String()] = nodeCatalogEntry{
		Services:   validServices,
		ReportedAt: time.Now(),
	}
	s.catalogMu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}
