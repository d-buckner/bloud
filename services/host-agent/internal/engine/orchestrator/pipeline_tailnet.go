// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// convergeTailnet ensures tailnet nodes, gateway, proxies, and the proxy
// outpost match the tailnet store state: an active connection brings a node up
// for every running app, and no connection takes the whole tailnet surface
// down.
func (o *Orchestrator) convergeTailnet(ctx context.Context) {
	if o.tailnetStore == nil {
		return
	}

	conn, err := o.tailnetStore.GetActive()
	if err != nil {
		o.logger.Error("failed to get active tailnet for convergence", "error", err)
		return
	}

	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Error("failed to list apps for tailnet convergence", "error", err)
		return
	}

	if conn != nil {
		o.ensureTailnetNodes(ctx, conn, apps)
		return
	}
	o.purgeTailnet(ctx, apps)
}

// ensureTailnetNodes brings a tailnet node up for each running non-system app
// and records which connection it now rides.
func (o *Orchestrator) ensureTailnetNodes(ctx context.Context, conn *store.TailnetConnection, apps []*store.InstalledApp) {
	o.logger.Info("tailnet active, ensuring nodes for running apps", "conn_id", conn.ID, "app_count", len(apps))
	if o.tailnetNode == nil {
		return
	}
	for _, app := range apps {
		if app.IsSystem || app.Status != "running" {
			continue
		}
		o.logger.Info("ensuring tailnet node", "app", app.CatalogID)
		if err := o.tailnetNode.EnsureRunning(ctx, app.CatalogID); err != nil {
			o.logger.Warn("failed to ensure tailnet node", "app", app.CatalogID, "error", err)
			continue
		}
		_ = o.appStore.SetTailnetID(app.CatalogID, conn.ID)
	}
}

// purgeTailnet takes the whole tailnet surface down: the per-app nodes, the
// SOCKS gateway, the proxy outpost, and the remote-proxy pool. Each piece logs
// and carries on, so one stuck container does not leave the others up.
func (o *Orchestrator) purgeTailnet(ctx context.Context, apps []*store.InstalledApp) {
	o.logger.Info("no active tailnet, purging nodes and gateway", "app_count", len(apps))
	if o.tailnetNode != nil {
		for _, app := range apps {
			if app.IsSystem {
				continue
			}
			o.logger.Info("purging tailnet node", "app", app.CatalogID)
			if err := o.tailnetNode.StopAndPurge(ctx, app.CatalogID); err != nil {
				o.logger.Warn("failed to purge tailnet node", "app", app.CatalogID, "error", err)
			}
			_ = o.appStore.SetTailnetID(app.CatalogID, "")
		}
	}
	if o.gateway != nil {
		if err := o.gateway.StopAndPurge(ctx); err != nil {
			o.logger.Warn("failed to purge gateway", "error", err)
		}
	}
	if o.proxyOutpost != nil {
		if err := o.proxyOutpost.Stop(ctx); err != nil {
			o.logger.Warn("failed to stop proxy outpost", "error", err)
		}
	}
	if o.remoteProxy != nil {
		o.remoteProxy.StopAll()
	}
}

// provisionTailnetSSO ensures a forward_domain Authentik proxy provider and standalone
// outpost exist for the tailnet MagicDNS domain. Best-effort: logs warnings on failure.
func (o *Orchestrator) provisionTailnetSSO(ctx context.Context) bool {
	if o.gateway == nil || o.forwardDomainSSO == nil {
		return false
	}

	if o.tailnetStore == nil {
		return false
	}
	conn, err := o.tailnetStore.GetActive()
	if err != nil || conn == nil {
		return false
	}

	o.logger.Info("convergence step", "step", "provision-tailnet-sso")

	domain, err := o.gateway.GetTailnetDomain(ctx)
	if err != nil {
		o.logger.Warn("failed to discover tailnet domain (gateway not ready?)", "error", err)
		return false
	}

	token, err := o.forwardDomainSSO.EnsureForwardDomainAuth(ctx, domain)
	if err != nil {
		o.logger.Warn("failed to provision tailnet forward_domain SSO", "error", err, "domain", domain)
		return false
	}

	if o.proxyOutpost != nil {
		if err := o.proxyOutpost.EnsureRunning(ctx, token, domain); err != nil {
			o.logger.Warn("failed to start proxy outpost", "error", err, "domain", domain)
			return false
		}
	}

	o.logger.Info("tailnet forward_domain SSO provisioned", "domain", domain)
	return true
}
