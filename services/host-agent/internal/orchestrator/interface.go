package orchestrator

// AppOrchestrator defines the interface for app lifecycle operations.
// The reconciler calls EnsureApp/RemoveApp for container lifecycle.
// RegenerateRoutes is called during convergence to rebuild Traefik config.
type AppOrchestrator interface {
	// RegenerateRoutes regenerates Traefik routes for all installed apps.
	RegenerateRoutes() error
}
