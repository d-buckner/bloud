// SPDX-License-Identifier: AGPL-3.0-only

package backrest

import (
	"encoding/json"
	"fmt"
)

// configVersion is the Backrest config-format version this seed targets. It is
// pinned, not derived: Backrest migrates any config below its own
// CurrentVersion (internal/config/migrations/migrations.go), and version 0 is
// rejected outright for a non-empty config, so an unversioned seed fails to
// start the app. Writing the version the pinned image uses means Backrest
// accepts the file as-is on first boot and only rewrites it to add the
// identity fields it populates itself. Bump this alongside the image pin when
// upstream advances the format.
const configVersion = 6

// defaultInstanceName identifies the snapshots this install creates. Backrest
// displays it in the UI and uses it to tell one instance's snapshots from
// another, which only matters once a second Backrest (a remote host) is paired.
const defaultInstanceName = "bloud"

// defaultRepoID / defaultPlanID are stable, human-readable ids. They appear in
// the UI and in the operations database, and a user can rename the plan (not
// the repo) later without the configurator touching it again.
const (
	defaultRepoID = "bloud-local"
	defaultPlanID = "app-folders"
)

// backrestConfig is the subset of Backrest's config.schema this seed writes.
// Backrest parses the file with protojson, so every field name and enum value
// here has to match its proto's json_name / enum name exactly; the local types
// are only a typed way to build that JSON. Anything Backrest needs and this
// does not set (the multihost identity) it populates on first load.
type backrestConfig struct {
	Version  int64          `json:"version"`
	Instance string         `json:"instance"`
	Repos    []backrestRepo `json:"repos"`
	Plans    []backrestPlan `json:"plans"`
	Auth     backrestAuth   `json:"auth"`
}

// backrestAuth disables Backrest's own login. The forward-auth proxy in front
// of the origin authenticates the browser, and nothing surfaces a generated
// Backrest password to the operator, so leaving the built-in login enabled
// would make the app unreachable through the proxy. See metadata.yaml for the
// published-port trade-off this accepts.
type backrestAuth struct {
	Disabled bool `json:"disabled"`
}

type backrestRepo struct {
	ID string `json:"id"`
	// URI is a restic repository location, here a path inside the container:
	// /repos is the mount of <dataDir>/backups.
	URI      string `json:"uri"`
	Password string `json:"password"`
	// AutoInitialize tells Backrest to run `restic init` the first time the
	// plan runs. It is the alternative to a guid (which a not-yet-created repo
	// cannot have); setting both is a validation error.
	AutoInitialize bool `json:"autoInitialize"`
	// PrunePolicy bounds unreferenced data. Monthly, on the last-run clock, so
	// a machine that was off for a while does not prune on every boot.
	PrunePolicy *backrestPrunePolicy `json:"prunePolicy,omitempty"`
}

type backrestPrunePolicy struct {
	Schedule backrestSchedule `json:"schedule"`
}

type backrestPlan struct {
	ID   string `json:"id"`
	Repo string `json:"repo"`
	// Paths are absolute inside the container. /bloud/apps is the read-only
	// mount of every app's private tree, so one plan covers them all, including
	// apps installed after this one.
	Paths []string `json:"paths"`
	// Excludes keeps Backrest out of its own state. /bloud/apps/backrest is the
	// same directory as /data, /config, and /cache; backing it up would nest
	// the operations database in every snapshot and copy the repository
	// password into the repository.
	Excludes  []string           `json:"excludes,omitempty"`
	Schedule  backrestSchedule   `json:"schedule"`
	Retention *backrestRetention `json:"retention,omitempty"`
}

// backrestSchedule is Backrest's schedule oneof plus its clock. Exactly one of
// Cron / MaxFrequencyDays is set: a cron expression for the daily backup, a
// maximum frequency in days for the monthly prune.
type backrestSchedule struct {
	Cron             string `json:"cron,omitempty"`
	MaxFrequencyDays int    `json:"maxFrequencyDays,omitempty"`
	// Clock is an enum: CLOCK_LOCAL for the backup (a wall-clock time the
	// operator can reason about) and CLOCK_LAST_RUN_TIME for the prune.
	Clock string `json:"clock,omitempty"`
}

type backrestRetention struct {
	// TimeBucketed serializes under its oneof member name, policyTimeBucketed.
	TimeBucketed backrestBuckets `json:"policyTimeBucketed"`
}

type backrestBuckets struct {
	Daily   int `json:"daily,omitempty"`
	Weekly  int `json:"weekly,omitempty"`
	Monthly int `json:"monthly,omitempty"`
}

// backupCron is 02:00 every day, local time. The schedule is editable in the
// UI; this is only the value the app installs with.
const backupCron = "0 2 * * *"

// renderConfig renders the seed config. The password is the app's own
// generated restic credential, never a literal.
func renderConfig(password string) ([]byte, error) {
	cfg := backrestConfig{
		Version:  configVersion,
		Instance: defaultInstanceName,
		Repos: []backrestRepo{{
			ID:             defaultRepoID,
			URI:            containerRepoDir + "/" + repoDirName,
			Password:       password,
			AutoInitialize: true,
			PrunePolicy: &backrestPrunePolicy{
				Schedule: backrestSchedule{
					MaxFrequencyDays: 30,
					Clock:            "CLOCK_LAST_RUN_TIME",
				},
			},
		}},
		Plans: []backrestPlan{{
			ID:       defaultPlanID,
			Repo:     defaultRepoID,
			Paths:    []string{containerAppDataDir},
			Excludes: []string{containerAppDataDir + "/" + appName},
			Schedule: backrestSchedule{Cron: backupCron, Clock: "CLOCK_LOCAL"},
			Retention: &backrestRetention{
				TimeBucketed: backrestBuckets{Daily: 7, Weekly: 4, Monthly: 6},
			},
		}},
		Auth: backrestAuth{Disabled: true},
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling the Backrest config: %w", err)
	}
	return append(out, '\n'), nil
}
