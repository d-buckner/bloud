// SPDX-License-Identifier: AGPL-3.0-only

package store

// AppStatus is the value of apps.status: the user-facing lifecycle projection
// of an installed app. It is derived from graph node status by the orchestrator
// (the single writer), not a convergence-control input; the graph node's status
// remains authoritative for convergence control (see
// docs/plans/operation-state-design.md §4).
type AppStatus string

// App status values. The set is closed: a status outside these five is a
// programming error at the store boundary, which is why callers use the named
// constants rather than string literals.
const (
	AppStatusInstalling   AppStatus = "installing"
	AppStatusUninstalling AppStatus = "uninstalling"
	AppStatusRunning      AppStatus = "running"
	AppStatusError        AppStatus = "error"
	AppStatusStopped      AppStatus = "stopped"
)
