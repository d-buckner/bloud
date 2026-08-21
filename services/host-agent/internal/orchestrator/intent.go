// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import "github.com/google/uuid"

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

// SetTailnetIntent requests configuring the tailnet connection.
type SetTailnetIntent struct {
	intentBase
	Name       string
	Type       string
	AuthKey    string
	ControlURL string
}

func (SetTailnetIntent) intentMarker() {}

func NewSetTailnetIntent(name, typ, authKey, controlURL string) SetTailnetIntent {
	return SetTailnetIntent{
		intentBase: newIntentBase(),
		Name:       name,
		Type:       typ,
		AuthKey:    authKey,
		ControlURL: controlURL,
	}
}

// DeleteTailnetIntent requests removal of the tailnet configuration.
type DeleteTailnetIntent struct {
	intentBase
}

func (DeleteTailnetIntent) intentMarker() {}

func NewDeleteTailnetIntent() DeleteTailnetIntent {
	return DeleteTailnetIntent{intentBase: newIntentBase()}
}

// AddRemoteAppIntent requests adding a remote app from another host.
type AddRemoteAppIntent struct {
	intentBase
	AppID       string
	TailnetAddr string
	HostLabel   string
}

func (AddRemoteAppIntent) intentMarker() {}

func NewAddRemoteAppIntent(appID, tailnetAddr, hostLabel string) AddRemoteAppIntent {
	return AddRemoteAppIntent{
		intentBase:  newIntentBase(),
		AppID:       appID,
		TailnetAddr: tailnetAddr,
		HostLabel:   hostLabel,
	}
}

// DeleteRemoteAppIntent requests removal of a remote app.
type DeleteRemoteAppIntent struct {
	intentBase
	RemoteAppID string
}

func (DeleteRemoteAppIntent) intentMarker() {}

func NewDeleteRemoteAppIntent(remoteAppID string) DeleteRemoteAppIntent {
	return DeleteRemoteAppIntent{intentBase: newIntentBase(), RemoteAppID: remoteAppID}
}

// CreateShareIntent requests sharing an app with other hosts.
type CreateShareIntent struct {
	intentBase
	AppName string
}

func (CreateShareIntent) intentMarker() {}

func NewCreateShareIntent(appName string) CreateShareIntent {
	return CreateShareIntent{intentBase: newIntentBase(), AppName: appName}
}

// RevokeShareIntent requests revoking an existing share.
type RevokeShareIntent struct {
	intentBase
	ShareID string
}

func (RevokeShareIntent) intentMarker() {}

func NewRevokeShareIntent(shareID string) RevokeShareIntent {
	return RevokeShareIntent{intentBase: newIntentBase(), ShareID: shareID}
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

// Compile-time assertions that all types implement Intent.
var (
	_ Intent = InstallAppIntent{}
	_ Intent = UninstallAppIntent{}
	_ Intent = RenameAppIntent{}
	_ Intent = SetTailnetIntent{}
	_ Intent = DeleteTailnetIntent{}
	_ Intent = AddRemoteAppIntent{}
	_ Intent = DeleteRemoteAppIntent{}
	_ Intent = CreateShareIntent{}
	_ Intent = RevokeShareIntent{}
	_ Intent = ClearAppDataIntent{}
)
