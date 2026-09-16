// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package jellyfin

import (
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/xmlutil"
)

// jellyfinNetworkConfig returns the desired XML config for network.xml.
var jellyfinNetworkConfig = xmlutil.ConfigValues{
	"PublishedServerUri":                "http://jellyfin.bloud.local",
	"EnablePublishedServerUriByRequest": "true",
	"KnownProxies":                      []string{"127.0.0.1", "::1"},
	"EnableHttps":                       "false",
	"RequireHttps":                      "false",
	"InternalHttpPort":                  "8096",
	"InternalHttpsPort":                 "8920",
	"PublicHttpPort":                    "8096",
	"PublicHttpsPort":                   "8920",
	"EnableRemoteAccess":                "true",
	"EnableIPv4":                        "true",
	"EnableIPv6":                        "false",
	"EnableUPnP":                        "false",
	"AutoDiscovery":                     "true",
	"IgnoreVirtualInterfaces":           "true",
}

// applyNetworkConfig applies jellyfinNetworkConfig to cfg if not already set.
// Returns true if changes were made.

// applyNetworkConfig applies jellyfinNetworkConfig to cfg if not already set.
// Returns true if changes were made.
func applyNetworkConfig(cfg *xmlutil.ConfigFile) bool {
	if cfg.HasConfig(jellyfinNetworkConfig) {
		return false
	}
	cfg.ApplyConfig(jellyfinNetworkConfig)
	return true
}

// configureNetwork creates or updates network.xml with reverse proxy settings.
// Returns true if the file content changed.

// configureNetwork creates or updates network.xml with reverse proxy settings.
// Returns true if the file content changed.
func (c *Configurator) configureNetwork(dataPath string) (bool, error) {
	networkPath := filepath.Join(dataPath, "config", "network.xml")

	cfg, err := xmlutil.Open(networkPath, "NetworkConfiguration")
	if err != nil {
		return false, err
	}

	if !applyNetworkConfig(cfg) {
		c.logger.Info("network.xml already configured for reverse proxy")
		return false, nil
	}

	changed, err := cfg.Save()
	if changed {
		c.logger.Info("network.xml configured for reverse proxy support")
	}
	return changed, err
}

// Remove is a no-op for the Jellyfin configurator; container and data removal
// are handled at a higher level by the orchestrator.
