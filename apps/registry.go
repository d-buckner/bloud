package apps

import "embed"

// FS contains all app metadata and icons embedded at build time.
//
//go:embed */metadata.yaml
//go:embed */icon.png
var FS embed.FS
