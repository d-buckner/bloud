// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
)

// fakeLister stands in for the podman client so the fast-gate decision can be
// exercised without a container runtime.
type fakeLister struct {
	containers []podman.Container
	err        error
}

func (f fakeLister) ListContainers(_ context.Context) ([]podman.Container, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.containers, nil
}

func TestSystemContainerNamesPicksUpOnlySystemApps(t *testing.T) {
	apps := []*catalog.App{
		{CatalogID: "traefik", IsSystem: true, Containers: []catalog.ContainerDef{{Name: "apps-traefik"}}},
		{
			CatalogID: "authentik",
			IsSystem:  true,
			Containers: []catalog.ContainerDef{
				{Name: "apps-authentik-postgres"},
				{Name: "apps-authentik-server"},
			},
		},
		// A user app must never widen the system set: its absence is not a
		// cold control plane.
		{CatalogID: "jellyfin", Containers: []catalog.ContainerDef{{Name: "apps-jellyfin"}}},
		nil,
	}

	got := systemContainerNames(apps)
	want := []string{"apps-authentik-postgres", "apps-authentik-server", "apps-traefik"}
	if len(got) != len(want) {
		t.Fatalf("systemContainerNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("systemContainerNames() = %v, want %v (sorted)", got, want)
		}
	}
}

func TestCheckWarmStack(t *testing.T) {
	required := []string{"apps-traefik", "apps-authentik-server"}

	t.Run("all running is complete", func(t *testing.T) {
		lister := fakeLister{containers: []podman.Container{
			{Names: []string{"apps-traefik"}, State: "running"},
			{Names: []string{"apps-authentik-server"}, State: "running"},
			{Names: []string{"apps-jellyfin"}, State: "running"},
		}}
		report, err := checkWarmStack(context.Background(), lister, required)
		if err != nil {
			t.Fatalf("checkWarmStack: %v", err)
		}
		if !report.Complete() {
			t.Fatalf("report = %+v, want complete", report)
		}
		if len(report.Running) != 2 {
			t.Fatalf("running = %v, want 2 entries", report.Running)
		}
	})

	t.Run("one stopped is incomplete and names it", func(t *testing.T) {
		lister := fakeLister{containers: []podman.Container{
			{Names: []string{"apps-traefik"}, State: "running"},
			{Names: []string{"apps-authentik-server"}, State: "exited"},
		}}
		report, err := checkWarmStack(context.Background(), lister, required)
		if err != nil {
			t.Fatalf("checkWarmStack: %v", err)
		}
		if report.Complete() {
			t.Fatalf("report = %+v, want incomplete", report)
		}
		if len(report.Missing) != 1 || report.Missing[0] != "apps-authentik-server" {
			t.Fatalf("missing = %v, want [apps-authentik-server]", report.Missing)
		}
	})

	t.Run("nothing running is incomplete", func(t *testing.T) {
		report, err := checkWarmStack(context.Background(), fakeLister{}, required)
		if err != nil {
			t.Fatalf("checkWarmStack: %v", err)
		}
		if report.Complete() {
			t.Fatalf("report = %+v, want incomplete", report)
		}
	})

	t.Run("a runtime error propagates instead of reading as cold", func(t *testing.T) {
		// Treating "cannot talk to podman" as "nothing is running" would be
		// the same wrong answer as a cold stack in every other respect, so it
		// has to surface as an error.
		_, err := checkWarmStack(context.Background(), fakeLister{err: errors.New("socket gone")}, required)
		if err == nil {
			t.Fatal("checkWarmStack = nil error, want the runtime error")
		}
	})

	t.Run("no required containers is never complete", func(t *testing.T) {
		// An empty requirement means the catalog produced no system apps,
		// which must not be read as "warm": that would open the gate with no
		// control plane at all.
		report, err := checkWarmStack(context.Background(), fakeLister{}, nil)
		if err != nil {
			t.Fatalf("checkWarmStack: %v", err)
		}
		if report.Complete() {
			t.Fatal("an empty required set must not report complete")
		}
	})
}

func TestDecideDevFastGate(t *testing.T) {
	warm := warmStackReport{
		Required: []string{"apps-traefik"},
		Running:  []string{"apps-traefik"},
	}
	cold := warmStackReport{
		Required: []string{"apps-traefik"},
		Missing:  []string{"apps-traefik"},
	}

	t.Run("warm stack and ready auth opens the gate", func(t *testing.T) {
		d := decideDevFastGate(warm, nil, true)
		if !d.Open {
			t.Fatalf("decision = %+v, want open", d)
		}
		if d.Reason == "" {
			t.Fatal("an open decision still needs a reason to log")
		}
	})

	t.Run("cold stack keeps the ordinary wait", func(t *testing.T) {
		d := decideDevFastGate(cold, nil, true)
		if d.Open {
			t.Fatalf("decision = %+v, want closed on a cold stack", d)
		}
		if d.Reason == "" {
			t.Fatal("a closed decision needs a reason the developer can read")
		}
	})

	t.Run("unready auth keeps the ordinary wait", func(t *testing.T) {
		// Opening the API without a working login would trade one kind of
		// broken dashboard for another.
		d := decideDevFastGate(warm, nil, false)
		if d.Open {
			t.Fatalf("decision = %+v, want closed without a ready OIDC client", d)
		}
	})

	t.Run("a failed check keeps the ordinary wait", func(t *testing.T) {
		d := decideDevFastGate(warmStackReport{}, errors.New("boom"), true)
		if d.Open {
			t.Fatalf("decision = %+v, want closed when the check failed", d)
		}
	})
}

func TestDevFastGateEnabled(t *testing.T) {
	if devFastGateEnabled(func(string) string { return "1" }) != true {
		t.Fatal("BLOUD_DEV_FAST_GATE=1 should enable the fast gate")
	}
	for _, v := range []string{"", "0", "true", "yes", "2"} {
		if devFastGateEnabled(func(string) string { return v }) {
			t.Errorf("value %q should not enable the fast gate", v)
		}
	}
}

func TestClosedGateIsAlreadyOpen(t *testing.T) {
	select {
	case <-closedGate():
	default:
		t.Fatal("closedGate() should return an already-closed (open) channel")
	}
}
