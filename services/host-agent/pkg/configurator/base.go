package configurator

import (
	"context"
	"fmt"
	"os"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
)

// BaseNodeLifecycle provides a default Remove implementation backed by a container
// runtime. App configurators may embed *BaseNodeLifecycle to inherit this behaviour;
// a nil pointer is safe (methods become no-ops).
//
// Name, PreStart, and PostStart are stub implementations; embedding structs override
// them with app-specific logic.
type BaseNodeLifecycle struct {
	containers container.Runtime
	spec       container.Spec
}

// NewBaseNodeLifecycle creates a BaseNodeLifecycle backed by the given runtime and spec.
func NewBaseNodeLifecycle(containers container.Runtime, spec container.Spec) *BaseNodeLifecycle {
	return &BaseNodeLifecycle{containers: containers, spec: spec}
}

// BaseConfigurator is an alias for BaseNodeLifecycle for backward compatibility.
// Deprecated: use BaseNodeLifecycle directly.
type BaseConfigurator = BaseNodeLifecycle

// NewBaseConfigurator creates a BaseNodeLifecycle. Deprecated: use NewBaseNodeLifecycle.
func NewBaseConfigurator(containers container.Runtime, spec container.Spec) *BaseNodeLifecycle {
	return NewBaseNodeLifecycle(containers, spec)
}

func (b *BaseNodeLifecycle) Name() string { return "" }

func (b *BaseNodeLifecycle) PreStart(_ context.Context, _ *AppState) error { return nil }

func (b *BaseNodeLifecycle) PostStart(_ context.Context, _ *AppState) error { return nil }

// Remove stops and removes the container, then optionally deletes the data directory.
// A nil receiver or nil containers runtime skips container removal.
func (b *BaseNodeLifecycle) Remove(ctx context.Context, state *AppState, clearData bool) error {
	if b != nil && b.containers != nil {
		if err := b.containers.Remove(ctx, b.spec.Name); err != nil {
			return fmt.Errorf("remove container: %w", err)
		}
	}
	if clearData && state != nil && state.DataPath != "" {
		if err := os.RemoveAll(state.DataPath); err != nil {
			return fmt.Errorf("remove app data: %w", err)
		}
	}
	return nil
}

// Verify BaseNodeLifecycle satisfies the NodeLifecycle interface.
var _ NodeLifecycle = (*BaseNodeLifecycle)(nil)
