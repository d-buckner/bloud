// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/cli/executor"
)

// healthExec answers Run as a curl -f against /api/health: it fails until
// readyAfter calls have been made, then succeeds.
type healthExec struct {
	mu         sync.Mutex
	calls      int
	readyAfter int
	commands   []string
}

func (h *healthExec) Run(_ context.Context, spec executor.RunSpec) (executor.ExecResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	h.commands = append(h.commands, spec.Command)
	if h.calls > h.readyAfter {
		return executor.ExecResult{}, nil
	}
	return executor.ExecResult{ExitCode: 22}, errors.New("exit status 22")
}

func (h *healthExec) RunStream(context.Context, executor.RunSpec, io.Writer, io.Writer) error {
	return errors.New("unused")
}
func (h *healthExec) CopyTo(context.Context, string, string) error   { return errors.New("unused") }
func (h *healthExec) CopyFrom(context.Context, string, string) error { return errors.New("unused") }

func TestAnnounceHostAgentReadyPrintsURLsOnceHealthy(t *testing.T) {
	ex := &healthExec{readyAfter: 3}
	var out bytes.Buffer

	announceHostAgentReady(context.Background(), ex, &out, map[string]string{"host-agent": "3000", "traefik": "8080"}, time.Millisecond, time.Hour)

	if ex.calls != 4 {
		t.Errorf("polled %d times, want 4 (3 failures then success)", ex.calls)
	}
	got := out.String()
	for _, want := range []string{"Bloud is ready", "http://localhost:8080", "http://localhost:3000"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q missing %q", got, want)
		}
	}
	if strings.Count(got, "Bloud is ready") != 1 {
		t.Errorf("ready line printed %d times, want once: %q", strings.Count(got, "Bloud is ready"), got)
	}
	if !strings.Contains(ex.commands[0], "http://localhost:3000/api/health") {
		t.Errorf("health command %q does not hit /api/health on the host-agent port", ex.commands[0])
	}
}

func TestAnnounceHostAgentReadyReportsProgressWhileWaiting(t *testing.T) {
	ex := &healthExec{readyAfter: 1 << 30}
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	announceHostAgentReady(ctx, ex, &out, nil, time.Millisecond, 10*time.Millisecond)

	got := out.String()
	if !strings.Contains(got, "Still starting apps") {
		t.Errorf("expected a progress line while waiting, got %q", got)
	}
	if strings.Contains(got, "Bloud is ready") {
		t.Errorf("must not announce ready when health never succeeded: %q", got)
	}
}

func TestAnnounceHostAgentReadyStopsOnCancelWithoutOutput(t *testing.T) {
	ex := &healthExec{readyAfter: 1 << 30}
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		announceHostAgentReady(ctx, ex, &out, nil, time.Millisecond, time.Hour)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("announceHostAgentReady did not return after cancel")
	}
	if out.Len() != 0 {
		t.Errorf("expected no output after early cancel, got %q", out.String())
	}
}

func TestPortOrFallsBackWhenBackendOmitsPort(t *testing.T) {
	if got := portOr(nil, "traefik", "8080"); got != "8080" {
		t.Errorf("portOr(nil) = %q, want fallback 8080", got)
	}
	if got := portOr(map[string]string{"traefik": "9090"}, "traefik", "8080"); got != "9090" {
		t.Errorf("portOr = %q, want reported 9090", got)
	}
}
