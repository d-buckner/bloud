// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type sseFrame struct {
	event string
	data  []byte
}

// readSSEFrames parses an SSE stream into {event, data} frames until the
// stream closes. Comment lines (the ": ping" heartbeat) are skipped.

// readSSEFrames parses an SSE stream into {event, data} frames until the
// stream closes. Comment lines (the ": ping" heartbeat) are skipped.
func readSSEFrames(body io.Reader, frames chan<- sseFrame) {
	defer close(frames)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // snapshot lines can be large
	var event string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			frames <- sseFrame{event: event, data: []byte(strings.TrimPrefix(line, "data: "))}
			event = ""
		case line == "":
			event = ""
		}
	}
}

// TestInstallLiveStateStream verifies the live-state contract:
//
//  1. The install 202 response carries the app record immediately (the
//     orchestrator records it at submit time), so the UI can render the
//     tile without polling.
//  2. The /api/apps/events SSE stream delivers a snapshot before any node
//     event, then node/pull updates, ending with the app node RUNNING.
//
// It must run before TestJellyfinInstallViaAPI (source order) so the install
// it drives is the fresh one.

// TestInstallLiveStateStream verifies the live-state contract:
//
//  1. The install 202 response carries the app record immediately (the
//     orchestrator records it at submit time), so the UI can render the
//     tile without polling.
//  2. The /api/apps/events SSE stream delivers a snapshot before any node
//     event, then node/pull updates, ending with the app node RUNNING.
//
// It must run before TestJellyfinInstallViaAPI (source order) so the install
// it drives is the fresh one.
func TestInstallLiveStateStream(t *testing.T) {
	// Open the SSE stream before submitting so no event is missed.
	sseResp := agentGet(t, hostAgentURL+"/api/apps/events")
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/apps/events: status %d", sseResp.StatusCode)
	}
	frames := make(chan sseFrame, 512)
	go readSSEFrames(sseResp.Body, frames)

	// POST install and inspect the 202 body.
	installResp := agentPost(t, hostAgentURL+"/api/apps/jellyfin/install", "application/json", strings.NewReader(`{}`))
	defer installResp.Body.Close()
	if installResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(installResp.Body)
		t.Fatalf("POST install: status %d: %s", installResp.StatusCode, body)
	}
	var install struct {
		IntentID string        `json:"intentId"`
		App      *installedApp `json:"app"`
	}
	if err := json.NewDecoder(installResp.Body).Decode(&install); err != nil {
		t.Fatalf("decode install response: %v", err)
	}
	if install.App == nil {
		t.Fatal("install 202 response must carry the app record (live-state contract)")
	}
	if install.App.Status != "installing" {
		t.Fatalf("install 202 app record status = %q, want \"installing\"", install.App.Status)
	}

	// Consume frames until the jellyfin node is RUNNING. Fresh VMs may pull
	// the image, so the budget matches TestJellyfinInstallViaAPI.
	sawSnapshot, snapshotBeforeNode, sawNode, sawPull, nodeRunning := false, true, false, false, false
	timeout := time.After(10 * time.Minute)
	for !nodeRunning {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("event stream closed before the app reached RUNNING")
			}
			switch f.event {
			case "snapshot":
				sawSnapshot = true
			case "node":
				// Node events carry the eventbus.NodeInfo projection
				// {app, container, phase}, not the raw graph node.
				var n struct {
					App       string `json:"app"`
					Container string `json:"container"`
					Phase     string `json:"phase"`
				}
				if err := json.Unmarshal(f.data, &n); err == nil && (n.Container == "apps-jellyfin" || n.App == "jellyfin") {
					sawNode = true
					if !sawSnapshot {
						snapshotBeforeNode = false
					}
					if n.Phase == "running" {
						nodeRunning = true
					}
				}
			case "pull":
				var p struct {
					App string `json:"app"`
				}
				if json.Unmarshal(f.data, &p) == nil && p.App == "jellyfin" {
					sawPull = true
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting for apps-jellyfin RUNNING on the event stream (sawSnapshot=%v sawNode=%v sawPull=%v)", sawSnapshot, sawNode, sawPull)
		}
	}

	if !sawSnapshot {
		t.Error("expected a snapshot event on the stream")
	}
	if !snapshotBeforeNode {
		t.Error("snapshot must be delivered before any node event")
	}
	if !sawNode {
		t.Error("expected node events for apps-jellyfin on the stream")
	}
	t.Logf("live-state stream: snapshot=%v node=%v pull=%v (pull absent if the image was already local)", sawSnapshot, sawNode, sawPull)
}

// --- Jellyfin through the real install path ---

// TestJellyfinInstallViaAPI installs Jellyfin through the host-agent API and
// waits for the orchestrator to converge it to running: intent queue,
// dependency graph, container creation, PreStart (LDAP plugin), PostStart
// (wizard, libraries, LDAP config), route generation.
