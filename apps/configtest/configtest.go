// SPDX-License-Identifier: AGPL-3.0-only

// Package configtest is the conformance harness for Bloud app configurators.
//
// NodeLifecycle documents what a configurator may do in each phase, but a doc
// comment enforces nothing, and thirteen apps were reading the same contract
// thirteen different ways. This package turns those promises into assertions
// that every app is held to, so a new app is covered the moment it registers
// and a change that breaks one of the rules fails a test instead of quietly
// becoming the new precedent.
//
// The harness asserts externally observable behaviour only. It calls the
// public contract and checks what a caller would notice: whether a recreate
// was requested, whether a network call happened, whether a second pass
// changed anything, whether the app's own metadata agrees with its code. It
// never inspects a private field or a call graph.
package configtest

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"gopkg.in/yaml.v3"
)

// Metadata mirrors the fields of an app's metadata.yaml that a configurator's
// own constants have to agree with. It is deliberately a partial view: the
// catalog loader owns the full schema, and duplicating it here would create a
// second definition to drift. These are only the fields that appear on both
// sides of the boundary between declarative metadata and configurator code.
type Metadata struct {
	Name       string              `yaml:"name"`
	Port       int                 `yaml:"port"`
	SSO        MetadataSSO         `yaml:"sso"`
	Containers []MetadataContainer `yaml:"containers"`
}

// MetadataSSO is the SSO block a configurator's callback path must match.
type MetadataSSO struct {
	Strategy     string `yaml:"strategy"`
	CallbackPath string `yaml:"callbackPath"`
}

// MetadataContainer is one container definition, enough to check a generated
// config file against the volume that mounts it.
type MetadataContainer struct {
	Name    string           `yaml:"name"`
	Volumes []MetadataVolume `yaml:"volumes"`
}

// MetadataVolume is one host-to-container mount.
type MetadataVolume struct {
	Source      string `yaml:"source"`
	Destination string `yaml:"destination"`
}

// LoadMetadata reads and parses the metadata.yaml in dir. The apps module
// cannot import the catalog loader (it lives under internal/), so the harness
// carries its own minimal reader. This is the pattern the Vaultwarden test
// introduced; generalising it replaces the hand-rolled line splitter the
// Paperless-ngx test used, which could not see nested keys at all.
func LoadMetadata(t *testing.T, dir string) Metadata {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "metadata.yaml"))
	if err != nil {
		t.Fatalf("read metadata.yaml in %s: %v", dir, err)
	}
	var md Metadata
	if err := yaml.Unmarshal(raw, &md); err != nil {
		t.Fatalf("parse metadata.yaml in %s: %v", dir, err)
	}
	return md
}

// Case is one configurator under test.
type Case struct {
	// Node is the graph node name the configurator is registered under.
	Node string

	// Cfg is the configurator instance under test. Leave it nil to have the
	// harness build it from the factory registered for Node, which is what
	// the table-driven suite does.
	Cfg configurator.NodeLifecycle

	// State builds the AppState for a pass against the given data dir. Each
	// assertion calls it fresh so no assertion inherits another's state.
	State func(dataDir, bloudDataDir string) *configurator.AppState

	// Metadata is the app's parsed metadata.yaml.
	Metadata Metadata

	// DefaultPort is the port the configurator's constructor falls back to
	// when registration passes 0. Checked against Metadata.Port.
	DefaultPort int

	// Preseed runs against a fresh data dir before each pass, for the few
	// apps whose PreStart short-circuits on a file it installs itself. HA
	// pins a remote component asset: seeding its manifest lets the install
	// skip instead of reaching the network, so the offline and idempotency
	// assertions test the contract rather than the download.
	Preseed func(dataDir string) error

	// AllowNetwork lets PreStart make HTTP calls. Every app should leave this
	// false; it exists so a documented exception is visible in the table
	// rather than absent from the checks.
	AllowNetwork bool

	// AllowRemover lists configurators that genuinely own teardown. A
	// configurator that implements Remover without being listed here is
	// advertising teardown it does not perform.
	AllowRemover bool
}

// buildConfigurator instantiates the configurator registered for node from
// the global factory registry using the supplied Deps. Each call gets a fresh
// registry so an instance cached by an earlier assertion cannot mask a
// problem in a later one.
func buildConfigurator(node string, deps configurator.Deps) (configurator.NodeLifecycle, error) {
	reg := configurator.NewRegistry(quietLogger(), deps)
	cfg := reg.Get(node)
	if cfg == nil {
		return nil, fmt.Errorf("no configurator registered for %q", node)
	}
	return cfg, nil
}

// Recorder counts the HTTP dials a configurator makes, so "PreStart touched
// the network" is an observed fact rather than an inference.
type Recorder struct {
	mu    sync.Mutex
	dials []string
}

func (r *Recorder) record(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dials = append(r.dials, addr)
}

func (r *Recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.dials)
}

func (r *Recorder) addresses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.dials))
	copy(out, r.dials)
	return out
}

// OfflineDeps returns a Deps whose HTTP transport records every dial and then
// fails it. A configurator that tries to reach the network during PreStart
// shows up in the recorder instead of reaching a real service.
//
// Scope note: this catches calls made through Deps.HTTP, which is the only
// sanctioned path. A configurator that opened a socket by hand would not be
// caught here, which is why the rule is also stated on the interface.
func OfflineDeps(t *testing.T) (configurator.Deps, *Recorder) {
	t.Helper()
	rec := &Recorder{}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			rec.record(addr)
			return nil, fmt.Errorf("configtest: network is blocked during PreStart (tried %s)", addr)
		},
	}
	deps := configurator.Deps{
		Logger: quietLogger(),
		HTTP: configurator.ClientFactory{
			Transport: transport,
			Logger:    quietLogger(),
		},
	}
	return deps, rec
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// Run applies every conformance assertion to one case.
func Run(t *testing.T, tc Case) {
	t.Helper()

	if tc.Cfg == nil {
		deps, _ := OfflineDeps(t)
		cfg, err := buildConfigurator(tc.Node, deps)
		if err != nil {
			t.Fatalf("%s: %v", tc.Node, err)
		}
		tc.Cfg = cfg
	}

	t.Run("name matches the registered node", func(t *testing.T) {
		AssertNodeName(t, tc)
	})
	t.Run("prestart is offline", func(t *testing.T) {
		AssertPreStartOffline(t, tc)
	})
	t.Run("prestart is idempotent", func(t *testing.T) {
		AssertPreStartIdempotent(t, tc)
	})
	t.Run("survives empty deps", func(t *testing.T) {
		AssertNilDepsSafe(t, tc)
	})
	t.Run("teardown is not a lie", func(t *testing.T) {
		AssertTeardownHonest(t, tc)
	})
	t.Run("port matches metadata", func(t *testing.T) {
		AssertPortMatchesMetadata(t, tc)
	})
}

// AssertNodeName checks that Name() reports the node the configurator is
// registered under. A mismatch means the orchestrator looks one place and the
// configurator answers another, which is the failure the registry exists to
// prevent.
func AssertNodeName(t *testing.T, tc Case) {
	t.Helper()
	if got := tc.Cfg.Name(); got != tc.Node {
		t.Errorf("Name() = %q, want the registered node %q", got, tc.Node)
	}
}

// AssertPreStartOffline checks that PreStart makes no network call. PreStart
// is the filesystem phase: config files, directories, certificates. A probe
// there couples a phase that must always succeed to a service that may not be
// up yet, and turns a slow or hanging app into a failed install.
func AssertPreStartOffline(t *testing.T, tc Case) {
	t.Helper()
	if tc.AllowNetwork {
		t.Skip("case declares PreStart may use the network")
	}

	deps, rec := OfflineDeps(t)
	cfg, err := buildConfigurator(tc.Node, deps)
	if err != nil {
		t.Fatalf("build configurator for %s: %v", tc.Node, err)
	}

	dir := t.TempDir()
	bloud := t.TempDir()
	if tc.Preseed != nil {
		if err := tc.Preseed(dir); err != nil {
			t.Fatalf("preseed %s: %v", tc.Node, err)
		}
	}
	state := tc.State(dir, bloud)

	_, _ = cfg.PreStart(context.Background(), state)

	if n := rec.count(); n != 0 {
		t.Errorf("PreStart made %d network call(s) %v; the phase is filesystem-only", n, rec.addresses())
	}
}

// AssertPreStartIdempotent checks that a second PreStart pass asks for no
// recreate. PreStart runs on every reconciliation cycle, so a configurator
// that reports a change every time would recreate its container forever.
func AssertPreStartIdempotent(t *testing.T, tc Case) {
	t.Helper()

	deps, _ := OfflineDeps(t)
	cfg, err := buildConfigurator(tc.Node, deps)
	if err != nil {
		t.Fatalf("build configurator for %s: %v", tc.Node, err)
	}

	dir := t.TempDir()
	bloud := t.TempDir()
	if tc.Preseed != nil {
		if err := tc.Preseed(dir); err != nil {
			t.Fatalf("preseed %s: %v", tc.Node, err)
		}
	}

	first, err := cfg.PreStart(context.Background(), tc.State(dir, bloud))
	if err != nil {
		t.Fatalf("first PreStart: %v", err)
	}
	// The first pass against an empty data dir is allowed to need a recreate.
	_ = first

	second, err := cfg.PreStart(context.Background(), tc.State(dir, bloud))
	if err != nil {
		t.Fatalf("second PreStart: %v", err)
	}
	if second.RestartNeeded {
		t.Errorf("second PreStart still asks for a recreate (reason %q); PreStart must converge", second.Reason)
	}
}

// AssertNilDepsSafe checks that a configurator built with a zero Deps does not
// panic. Factories are documented to tolerate a missing secrets provider and a
// missing runtime, because CLI and test contexts supply neither.
func AssertNilDepsSafe(t *testing.T, tc Case) {
	t.Helper()

	cfg, err := buildConfigurator(tc.Node, configurator.Deps{})
	if err != nil {
		// A factory that refuses to build with nothing is a clean answer.
		t.Skipf("factory for %s declines empty deps: %v", tc.Node, err)
	}

	dir := t.TempDir()
	bloud := t.TempDir()
	state := tc.State(dir, bloud)

	// The assertion is the absence of a panic; the error is not the point.
	_, _ = cfg.PreStart(context.Background(), state)
}

// AssertTeardownHonest checks that a configurator implementing Remover is one
// that was declared to own teardown. The orchestrator type-asserts the
// interface, so a no-op implementation is dead code that still advertises
// "this app owns its teardown" to everyone reading the type.
func AssertTeardownHonest(t *testing.T, tc Case) {
	t.Helper()
	_, implements := tc.Cfg.(configurator.Remover)
	if implements && !tc.AllowRemover {
		t.Errorf("%s implements configurator.Remover but is not declared a teardown owner; "+
			"either drop the method (the orchestrator removes containers and data itself) "+
			"or declare it as an owner", tc.Node)
	}
}

// AssertPortMatchesMetadata checks the app's defaultPort against the port the
// catalog publishes. They are the same fact written twice, and only one of
// them routes traffic.
func AssertPortMatchesMetadata(t *testing.T, tc Case) {
	t.Helper()
	if tc.Metadata.Port == 0 {
		t.Skip("no port in metadata")
	}
	if tc.DefaultPort == 0 {
		t.Skip("case does not declare its default port")
	}
	if tc.DefaultPort != tc.Metadata.Port {
		t.Errorf("configurator defaultPort = %d, metadata.yaml port = %d; these must be the same value",
			tc.DefaultPort, tc.Metadata.Port)
	}
}
