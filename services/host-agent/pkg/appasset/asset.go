// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package appasset installs static and downloaded files into an app's data
// directory with one hardened implementation: fetch (with retry) → verify
// (sha256) → stage → atomic commit. It knows archives and digests, nothing
// about any particular app.
//
// The download path is retried, so a transient CDN failure no longer bricks an
// install into a terminal node ERROR. The commit is atomic (temp + rename in
// the destination filesystem), so a failed install never leaves a half tree.
package appasset

import (
	"io/fs"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// Kind selects how the fetched payload is committed.
type Kind int

const (
	// File commits the payload as a single file at Dest.
	File Kind = iota
	// Zip unpacks the payload (a zip archive) into Dest.
	Zip
)

// Source describes where an asset's bytes come from. Exactly one field is set.
type Source struct {
	// URL is a remote fetch (retried + cached by SHA256).
	URL string
	// Embed is a go:embed filesystem in the app package.
	Embed fs.FS
	// Name is the file name within Embed (required with Embed).
	Name string
	// Local is a host path (transitional — prefer Embed).
	Local string
}

// URL builds a remote-fetch source.
func URL(u string) Source { return Source{URL: u} }

// Embedded builds a source from a go:embed filesystem and a name within it.
func Embedded(fsys fs.FS, name string) Source { return Source{Embed: fsys, Name: name} }

// Local builds a source from a host path.
func Local(path string) Source { return Source{Local: path} }

// Asset is one static-file install unit.
type Asset struct {
	// Name is the log + marker identity ("hass-oidc-auth").
	Name string
	// Dest is the absolute target (a dir for Zip, a file for File).
	Dest string
	// Source is where the bytes come from.
	Source Source
	// Kind is File or Zip.
	Kind Kind
	// SHA256 is the expected digest. Required for URL sources; empty is a
	// hard error there. Optional for Embed/Local.
	SHA256 string
	// Strip is the number of leading path components to strip when unpacking
	// a Zip.
	Strip int
	// Sentinel is a path relative to Dest that must exist after install. It
	// doubles as the default "already installed" check.
	Sentinel string
	// SkipIf overrides the sentinel check (e.g. a version-aware skip).
	SkipIf func(dest string) bool
	// Verify runs on the staged tree before commit; a failure aborts and
	// nothing lands. (Zip only.)
	Verify func(staging string) error
	// MaxBytes caps the download. Default 64 MiB.
	MaxBytes int64
	// Retry overrides the installer's retry policy for the fetch.
	Retry appclient.RetryPolicy
}

// defaultMaxBytes is the download cap when Asset.MaxBytes is unset.
const defaultMaxBytes int64 = 64 * 1024 * 1024
