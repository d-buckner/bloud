// SPDX-License-Identifier: AGPL-3.0-only

// Package sonarr wires the Sonarr PVR into Bloud.
//
// The lifecycle itself is shared: Sonarr and Radarr are the same program with
// a different library, so pre-seeding config.xml, verifying external auth
// through the app's own API, registering the root folder, and wiring the
// qBittorrent download client all live in pkg/servarr. What is left here is
// only what makes Sonarr Sonarr: its port, its library mount, and the
// category its download client stores.
package sonarr

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/servarr"
)

// appName is the catalog id and the identity the API client logs under.
const appName = "sonarr"

// app is the whole of Sonarr's difference from the shared Servarr lifecycle.
// Sonarr and Radarr share API v3; Prowlarr still answers on v1.
var app = servarr.PVRApp{
	Name:                  appName,
	APIPath:               "api/v3",
	DefaultPort:           8989,
	ConfigFileName:        "config.xml",
	MediaSubdir:           "shows",
	RootFolderMount:       "/shows",
	DownloadCategoryField: "tvCategory",
	DownloadCategory:      "tv-sonarr",
}

// nodeName is the graph node / container name the host-agent reconciles. It is
// derived rather than written out so the registration key and the
// configurator's own Name() cannot drift apart.
var nodeName = app.NodeName()

// Configurator handles Sonarr configuration.
type Configurator = servarr.PVRConfigurator

// NewConfigurator creates a new Sonarr configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	return servarr.NewPVRConfigurator(app, port, deps)
}
