// SPDX-License-Identifier: AGPL-3.0-only

package inference

import (
	"log/slog"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// MigrateUpstreamsToExternal moves the AI upstreams out of the settings KV and
// into the external provider registry, and the single instance-scoped API key
// onto the record of the upstream it was used with.
//
// It runs at startup, before the orchestrator builds, because the resolver reads
// the new location and a half-moved list would read as an instance with no AI
// configured.
//
// The migration is its own marker: `ai_upstreams` is deleted last, after every
// row and every credential is written. A crash partway therefore re-runs the
// whole thing on the next boot rather than leaving a list that exists in neither
// place, and the write path is an upsert keyed on the upstream's own stable ID,
// so running it twice changes nothing the second time.
//
// The old key is deleted rather than left behind. A secret nothing reads is a
// secret someone will eventually find and trust to still mean something.
func MigrateUpstreamsToExternal(
	settings store.SettingsStoreInterface,
	external store.ExternalAppStoreInterface,
	secrets configurator.AppSecretsProvider,
	logger *slog.Logger,
) error {
	if settings == nil || external == nil {
		return nil
	}

	upstreamsJSON, err := settings.Get(SettingUpstreams)
	if err != nil || upstreamsJSON == "" {
		// Absent means already migrated, or never configured. Both are a no-op.
		return nil
	}
	settingsVal, err := DecodeSettings(upstreamsJSON, "")
	if err != nil {
		// An unreadable legacy value is not a reason to refuse to boot. Leave it
		// where it is and say so loudly rather than deleting data nobody can read.
		logger.Warn("legacy AI upstreams are unreadable; leaving them in place", "error", err)
		return nil
	}
	if len(settingsVal.Upstreams) == 0 {
		return deleteLegacyUpstreams(settings, secrets, logger)
	}

	// The old model carried one key for whichever upstream was active. It moves
	// to that upstream's record alone; the others get none, which is the truth
	// the single-field UI never recorded.
	legacyKey := ""
	activeID := ""
	if active, ok := settingsVal.ActiveUpstream(); ok {
		activeID = active.ID
	}
	if secrets != nil {
		legacyKey = secrets.GetAppSecret(SecretScope, SecretAPIKey)
	}

	for _, u := range settingsVal.Upstreams {
		if err := external.Upsert(ExternalForUpstream(u)); err != nil {
			return err
		}
		if legacyKey != "" && u.ID == activeID {
			if err := secrets.SetAppSecret(store.ExternalSecretScope(u.ID), ContractName, legacyKey); err != nil {
				return err
			}
		}
	}

	if err := deleteLegacyUpstreams(settings, secrets, logger); err != nil {
		return err
	}
	logger.Info("migrated AI upstreams into the external provider registry",
		"upstreams", len(settingsVal.Upstreams),
		"active", activeID,
		"credentialMoved", legacyKey != "")
	return nil
}

func deleteLegacyUpstreams(settings store.SettingsStoreInterface, secrets configurator.AppSecretsProvider, logger *slog.Logger) error {
	// Setting an empty string is the store's documented way to clear a key, and
	// an empty `ai_upstreams` reads as "nothing to migrate" on the next boot.
	if err := settings.Set(SettingUpstreams, ""); err != nil {
		return err
	}
	if secrets == nil {
		return nil
	}
	if err := secrets.DeleteAppSecrets(SecretScope); err != nil {
		logger.Warn("migrated AI upstreams but could not remove the legacy instance-scoped key", "error", err)
	}
	return nil
}
