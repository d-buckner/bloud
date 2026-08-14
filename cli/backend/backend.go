// Package backend abstracts provisioning and lifecycle management of runtime
// environments (Lima VM, native box, etc.).
package backend

import (
	"context"

	"codeberg.org/d-buckner/bloud/cli/executor"
)

// Backend provisions a runtime environment and exposes its Host once ready.
type Backend interface {
	// Create ensures the environment exists and is running.
	Create(ctx context.Context) error
	// Destroy tears the environment down.
	Destroy(ctx context.Context) error
	// Host returns the runtime host for executing commands.
	Host() executor.Host
	// SyncProject copies the host project into the guest.
	SyncProject(ctx context.Context) error
}
