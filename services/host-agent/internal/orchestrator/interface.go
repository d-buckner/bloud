package orchestrator

import "context"

// AppOrchestrator defines the interface for app installation orchestrators
type AppOrchestrator interface {
	// Install installs an app with the given choices
	Install(ctx context.Context, req InstallRequest) (InstallResponse, error)

	// Uninstall removes an app
	Uninstall(ctx context.Context, req UninstallRequest) (UninstallResponse, error)

	// RegenerateRoutes regenerates Traefik routes for all installed apps
	// This should be called on startup to ensure routes are in sync
	RegenerateRoutes() error
}

// InstallRequest specifies what to install and how
type InstallRequest struct {
	App     string            `json:"app"`
	Choices map[string]string `json:"choices"` // integration -> chosen app
}

// UninstallRequest specifies what to uninstall and how
type UninstallRequest struct {
	App       string `json:"app"`
	ClearData bool   `json:"clearData"` // If true, also delete data directory and database
}

// UninstallResult describes the outcome of an uninstallation
type UninstallResult struct {
	Success      bool     `json:"success"`
	App          string   `json:"app"`
	Error        string   `json:"error,omitempty"`
	Unconfigured []string `json:"unconfigured,omitempty"` // Apps that will be unconfigured
}

// InstallResponse is the common interface for install results
type InstallResponse interface {
	IsSuccess() bool
	GetApp() string
	GetError() string
}

// UninstallResponse is the common interface for uninstall results
type UninstallResponse interface {
	IsSuccess() bool
	GetApp() string
	GetError() string
}

// Ensure types implement interfaces
var _ InstallResponse = (*InstallResult)(nil)
var _ UninstallResponse = (*UninstallResult)(nil)

// InstallResult methods
func (r *InstallResult) IsSuccess() bool  { return r.Success }
func (r *InstallResult) GetApp() string   { return r.App }
func (r *InstallResult) GetError() string { return r.Error }

// UninstallResult methods
func (r *UninstallResult) IsSuccess() bool  { return r.Success }
func (r *UninstallResult) GetApp() string   { return r.App }
func (r *UninstallResult) GetError() string { return r.Error }
