package integration

// AppID identifies an application independently of its runtime representation.
type AppID string

// Type identifies a provider-consumer relationship, such as database or cache.
type Type string

// Requirement describes the providers that can satisfy an integration.
type Requirement struct {
	Required   bool
	Compatible []AppID
}

// Bindings maps each resolved integration type to its provider.
type Bindings map[Type]AppID

// ResolutionInput contains the pure inputs needed to resolve integrations.
type ResolutionInput struct {
	Requirements   map[Type]Requirement
	BoundProviders map[Type]AppID
	Installed      map[AppID]struct{}
}
