// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package configurator

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

// stubConfigurator is a minimal NodeLifecycle for registry tests.
type stubConfigurator struct{ name string }

func (s *stubConfigurator) Name() string { return s.name }
func (s *stubConfigurator) PreStart(context.Context, *AppState) (bool, error) {
	return false, nil
}
func (s *stubConfigurator) PostStart(context.Context, *AppState) error    { return nil }
func (s *stubConfigurator) Remove(context.Context, *AppState, bool) error { return nil }

// unregisterFactoryForTest removes a test factory from the global registry.
func unregisterFactoryForTest(nodeName string) {
	factoryMu.Lock()
	defer factoryMu.Unlock()
	delete(nodeFactories, nodeName)
}

func testRegistry() *Registry {
	return NewRegistry(slog.New(slog.DiscardHandler), Deps{})
}

func TestRegistryLazyFactoryInstantiation(t *testing.T) {
	calls := 0
	RegisterFactory("test-lazy-app", func(deps Deps) (NodeLifecycle, error) {
		calls++
		return &stubConfigurator{name: "test-lazy-app"}, nil
	})
	t.Cleanup(func() { unregisterFactoryForTest("test-lazy-app") })

	r := testRegistry()

	// Has returns true without instantiating.
	if !r.Has("test-lazy-app") {
		t.Fatal("Has() should report factory-registered apps")
	}
	if calls != 0 {
		t.Fatal("Has() must not instantiate the factory")
	}

	built := r.Get("test-lazy-app")
	if built == nil {
		t.Fatal("Get() should instantiate the registered factory")
	}
	if calls != 1 {
		t.Fatalf("factory called %d times after first Get, want 1", calls)
	}

	// Subsequent Get returns the same cached instance (factory runs once).
	again := r.Get("test-lazy-app")
	if built != again {
		t.Fatal("Get() should return the cached instance, not re-instantiate")
	}
	if calls != 1 {
		t.Fatalf("factory called %d times after second Get, want 1", calls)
	}

	// Names() includes factory-registered apps.
	var found bool
	for _, n := range r.Names() {
		if n == "test-lazy-app" {
			found = true
		}
	}
	if !found {
		t.Fatal("Names() should include factory-registered apps")
	}
}

func TestRegistryFactoryError(t *testing.T) {
	RegisterFactory("test-fail-app", func(deps Deps) (NodeLifecycle, error) {
		return nil, errors.New("boom")
	})
	t.Cleanup(func() { unregisterFactoryForTest("test-fail-app") })

	r := testRegistry()
	if c := r.Get("test-fail-app"); c != nil {
		t.Fatal("Get() should return nil when the factory fails")
	}
}

func TestRegistryUnknownApp(t *testing.T) {
	r := testRegistry()
	if r.Has("test-nonexistent-app") {
		t.Fatal("Has() should be false for unknown apps")
	}
	if c := r.Get("test-nonexistent-app"); c != nil {
		t.Fatal("Get() should return nil for unknown apps")
	}
}

func TestRegistryRegisterTakesPrecedenceOverFactory(t *testing.T) {
	RegisterFactory("test-override-app", func(deps Deps) (NodeLifecycle, error) {
		t.Error("factory should not run when an instance is registered directly")
		return &stubConfigurator{name: "test-override-app"}, nil
	})
	t.Cleanup(func() { unregisterFactoryForTest("test-override-app") })

	r := testRegistry()
	direct := &stubConfigurator{name: "test-override-app"}
	r.Register(direct)

	if got := r.Get("test-override-app"); got != NodeLifecycle(direct) {
		t.Fatal("Register() instance should take precedence over the factory")
	}
}
