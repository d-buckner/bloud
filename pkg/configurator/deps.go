// SPDX-License-Identifier: AGPL-3.0-only

package configurator

import (
	"context"

	"codeberg.org/d-buckner/bloud/pkg/appasset"
	"codeberg.org/d-buckner/bloud/pkg/appclient"
)

// HTTPFactory builds app HTTP clients. The SDK declares this interface; the
// host injects the concrete appclient.Factory implementation. A configurator
// calls New to get a client for its own API surface.
//
// The field Deps.HTTP is nil when the host did not inject a factory, which
// happens in CLI and test contexts. A configurator that needs one must treat
// nil as "not available" and skip the client it would have built, the same way
// it treats a nil Secrets or Runtime callback.
type HTTPFactory interface {
	New(spec appclient.Spec) *appclient.Client
}

// AssetInstaller installs static or downloaded assets with the shared content
// cache. The SDK declares this interface; the host injects the concrete
// appasset.Installer implementation.
//
// The field Deps.Assets is nil when the host did not inject an installer. A
// configurator that installs assets must treat nil as "cannot apply now" and
// skip the install, the same way it treats a nil Runtime callback.
type AssetInstaller interface {
	Install(ctx context.Context, a appasset.Asset) (bool, error)
}
