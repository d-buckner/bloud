package integration

// AppID identifies an application independently of its runtime representation.
type AppID string

// Type identifies a provider-consumer relationship, such as database or cache.
type Type string

// ResolutionSource describes how an integration provider was selected.
type ResolutionSource string

const (
	ResolutionBound    ResolutionSource = "bound"
	ResolutionOptional ResolutionSource = "optional-installed"
)

// Requirement describes the providers that can satisfy an integration.
type Requirement struct {
	Required   bool
	Compatible []AppID
}

// Instance is a resolved provider-consumer relationship.
type Instance struct {
	Consumer AppID
	Provider AppID
	Type     Type
	Required bool
	Source   ResolutionSource
}

// ResolutionInput contains the pure inputs needed to resolve integrations.
type ResolutionInput struct {
	Consumer       AppID
	Requirements   map[Type]Requirement
	BoundProviders map[Type]AppID
	Installed      map[AppID]bool
}
