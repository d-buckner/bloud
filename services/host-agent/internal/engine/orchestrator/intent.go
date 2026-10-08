// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"github.com/google/uuid"
)

// Intent represents a mutation request to be processed by the orchestrator.
// The interface is sealed via the unexported intentMarker() method.
type Intent interface {
	intentMarker()
	IntentID() string
}

// intentBase provides common fields for all intent types.
type intentBase struct {
	ID string
}

func newIntentBase() intentBase {
	return intentBase{ID: uuid.New().String()}
}

func (b intentBase) IntentID() string { return b.ID }

// InstallAppIntent requests installation of an app.
type InstallAppIntent struct {
	intentBase
	AppName string
}

func (InstallAppIntent) intentMarker() {}

func NewInstallAppIntent(appName string) InstallAppIntent {
	return InstallAppIntent{intentBase: newIntentBase(), AppName: appName}
}

// UninstallAppIntent requests removal of an app.
type UninstallAppIntent struct {
	intentBase
	AppName   string
	ClearData bool
}

func (UninstallAppIntent) intentMarker() {}

func NewUninstallAppIntent(appName string, clearData bool) UninstallAppIntent {
	return UninstallAppIntent{intentBase: newIntentBase(), AppName: appName, ClearData: clearData}
}

// RenameAppIntent requests changing an app's display name.
type RenameAppIntent struct {
	intentBase
	AppName     string
	DisplayName string
}

func (RenameAppIntent) intentMarker() {}

func NewRenameAppIntent(appName string, displayName string) RenameAppIntent {
	return RenameAppIntent{intentBase: newIntentBase(), AppName: appName, DisplayName: displayName}
}

// ClearAppDataIntent requests clearing an app's data directory.
type ClearAppDataIntent struct {
	intentBase
	AppName string
}

func (ClearAppDataIntent) intentMarker() {}

func NewClearAppDataIntent(appName string) ClearAppDataIntent {
	return ClearAppDataIntent{intentBase: newIntentBase(), AppName: appName}
}

// RevokeClientSessionsIntent asks the orchestrator to end every live session
// an app currently holds. It is the counterpart to rotating a credential, and
// the two are deliberately separate: rotation changes what authenticates future
// sign-ins, revocation ends the ones that already exist.
//
// The request has to go through the orchestrator rather than straight to the
// container because clearing a running app's session store is a side effect on
// a managed container, and invariant 1 makes the orchestrator the only
// executor of those.
type RevokeClientSessionsIntent struct {
	intentBase
	AppName string
}

func (RevokeClientSessionsIntent) intentMarker() {}

func NewRevokeClientSessionsIntent(appName string) RevokeClientSessionsIntent {
	return RevokeClientSessionsIntent{intentBase: newIntentBase(), AppName: appName}
}

// SetPublicURLIntent requests changing the address this Bloud is reachable
// at, given as one origin: https://bloud.example.com:8443. The orchestrator
// validates it, persists it, updates the runtime URL state, and resets
// SSO-dependent nodes so the convergence pass re-provisions SSO and
// rewrites app configs with the new URLs.
//
// The URL travels as a string rather than a parsed value so the intent stays
// a plain serializable record of what was asked; parsing happens once, in the
// orchestrator, which is the only place that decides what is valid.
type SetPublicURLIntent struct {
	intentBase
	URL string
}

func (SetPublicURLIntent) intentMarker() {}

func NewSetPublicURLIntent(rawURL string) SetPublicURLIntent {
	return SetPublicURLIntent{intentBase: newIntentBase(), URL: rawURL}
}

// SetInferenceIntent requests changing the instance's AI configuration: the
// upstream list, the default model every consumer adopts unless it has chosen
// its own, and optionally the upstream credential.
//
// The credential is a *string because the API distinguishes "leave what is
// stored alone" (nil) from "clear it" (pointer to an empty string), and a plain
// string cannot carry that difference. It travels here rather than being written
// by the handler so the secrets manager is only ever touched by the
// orchestrator, which is the single writer.
type SetInferenceIntent struct {
	intentBase
	// UpstreamsJSON is the encoded inference.Upstream list.
	UpstreamsJSON string
	// DefaultModel is the instance default model id.
	DefaultModel string
	// APIKey is the upstream credential. nil keeps the stored value.
	APIKey *string
}

func (SetInferenceIntent) intentMarker() {}

func NewSetInferenceIntent(upstreamsJSON, defaultModel string, apiKey *string) SetInferenceIntent {
	return SetInferenceIntent{
		intentBase:    newIntentBase(),
		UpstreamsJSON: upstreamsJSON,
		DefaultModel:  defaultModel,
		APIKey:        apiKey,
	}
}

// ReconcileIntent requests a self-healing convergence pass. It carries no
// request of its own: the value is the pass that runs after the drain, not
// anything this intent mutates. It exists so the periodic pass enters the
// engine through the same single-writer queue as every user action
// (invariant 1) instead of a second goroutine calling converge directly.
//
// Submitting one is also how a retryable failure gets retried: the drain arm
// resets ERROR nodes whose operation row says a retry may help, which is the
// driver behind that flag. See Orchestrator.retryErroredNodes.
type ReconcileIntent struct {
	intentBase
}

func (ReconcileIntent) intentMarker() {}

func NewReconcileIntent() ReconcileIntent {
	return ReconcileIntent{intentBase: newIntentBase()}
}

// ExternalAppSpec is the whole operator-declared record behind an add or
// update intent. A launcher fills the identity fields and leaves the provider
// fields zero; a provider also names its source and carries the per-contract
// values and credentials the operator typed.
//
// The shape is the same for both kinds on purpose. One intent type per kind
// would fork the plumbing for a difference that is only which fields are set,
// and the applier would end up branching anyway.
type ExternalAppSpec struct {
	ID     string
	Kind   string
	Source string
	Name   string
	URL    string
	Icon   string
	// Values holds the operator-supplied non-secret contract values, keyed by
	// contract name and then by the value key that contract declares. It is
	// what stands in for the runtime values a local provider's configurator
	// would have minted.
	Values map[string]map[string]string
	// Secrets holds the operator-supplied credentials keyed by the contract
	// they belong to. The applier writes them into the external scope of the
	// secrets manager and never into the external_apps row.
	Secrets map[string]string
}

// AddExternalAppIntent requests adding an external app: a launcher tile, or a
// provider record that stands in for a catalog app Bloud does not run.
//
// It carries no container, route, or SSO work because there is none to do. The
// applier persists the record and its credentials and nothing else, which is
// what keeps an external app out of the convergence layer entirely.
type AddExternalAppIntent struct {
	intentBase
	Spec ExternalAppSpec
}

func (AddExternalAppIntent) intentMarker() {}

func NewAddExternalAppIntent(spec ExternalAppSpec) AddExternalAppIntent {
	return AddExternalAppIntent{intentBase: newIntentBase(), Spec: spec}
}

// UpdateExternalAppIntent requests changing an external app. It carries the
// full spec rather than a patch, so a provider's credentials and values can be
// edited through the same call that edits its name, and "field omitted" never
// has to mean two different things.
type UpdateExternalAppIntent struct {
	intentBase
	Spec ExternalAppSpec
}

func (UpdateExternalAppIntent) intentMarker() {}

func NewUpdateExternalAppIntent(spec ExternalAppSpec) UpdateExternalAppIntent {
	return UpdateExternalAppIntent{intentBase: newIntentBase(), Spec: spec}
}

// RemoveExternalAppIntent requests deleting an external app.
type RemoveExternalAppIntent struct {
	intentBase
	ID string
}

func (RemoveExternalAppIntent) intentMarker() {}

func NewRemoveExternalAppIntent(id string) RemoveExternalAppIntent {
	return RemoveExternalAppIntent{intentBase: newIntentBase(), ID: id}
}

// Compile-time assertions that all types implement Intent.
var (
	_ Intent = InstallAppIntent{}
	_ Intent = UninstallAppIntent{}
	_ Intent = RenameAppIntent{}
	_ Intent = ClearAppDataIntent{}
	_ Intent = SetPublicURLIntent{}
	_ Intent = SetInferenceIntent{}
	_ Intent = ReconcileIntent{}
	_ Intent = AddExternalAppIntent{}
	_ Intent = UpdateExternalAppIntent{}
	_ Intent = RemoveExternalAppIntent{}
)
