// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

// nfpmVersion pins the packager used to build the .deb, matching the repo's
// convention of pinning build tools by version (golangci-lint, vale).
const nfpmVersion = "v2.47.0"

// packageConfig holds the resolved inputs for a `bloud package` run.
type packageConfig struct {
	root    string
	arch    string
	version string
	outDir  string
}

func cmdPackage(args []string) int {
	root, err := getProjectRoot()
	if err != nil {
		errorf("could not find project root: %v", err)
		return 1
	}

	cfg := packageConfig{
		root:    root,
		arch:    runtime.GOARCH,
		version: packageVersion(root),
		outDir:  filepath.Join(root, "dist"),
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--arch":
			if i+1 < len(args) {
				i++
				cfg.arch = args[i]
			}
		case "--version":
			if i+1 < len(args) {
				i++
				cfg.version = args[i]
			}
		case "--out":
			if i+1 < len(args) {
				i++
				cfg.outDir = args[i]
			}
		default:
			errorf("unknown flag %q (usage: bloud package [--arch amd64|arm64] [--version V] [--out DIR])", args[i])
			return 1
		}
	}

	if err := runPackage(cfg); err != nil {
		errorf("package failed: %v", err)
		return 1
	}
	return 0
}

// runPackage stages the release payload and hands it to nfpm to emit a .deb.
func runPackage(cfg packageConfig) error {
	stageDir := filepath.Join(cfg.outDir, "payload")
	if err := os.RemoveAll(stageDir); err != nil {
		return fmt.Errorf("clean staging dir: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}

	log("Building host-agent for linux/" + cfg.arch)
	hostAgentDir := filepath.Join(cfg.root, "services", "host-agent")
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w",
		"-o", filepath.Join(stageDir, "host-agent"), "./cmd/host-agent")
	build.Dir = hostAgentDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+cfg.arch)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build host-agent: %w", err)
	}

	log("Building frontend")
	web := exec.Command("npm", "run", "build", "--workspace=@bloud/host-agent-web")
	web.Dir = cfg.root
	web.Stdout = os.Stdout
	web.Stderr = os.Stderr
	if err := web.Run(); err != nil {
		return fmt.Errorf("build frontend: %w", err)
	}
	webBuild := filepath.Join(hostAgentDir, "web", "build")
	if _, err := os.Stat(webBuild); err != nil {
		return fmt.Errorf("frontend build missing at %s: %w", webBuild, err)
	}
	if err := copyTree(webBuild, filepath.Join(stageDir, "web", "build")); err != nil {
		return fmt.Errorf("stage frontend: %w", err)
	}

	log("Staging app catalog")
	if err := stageCatalog(filepath.Join(cfg.root, "apps"), filepath.Join(stageDir, "apps")); err != nil {
		return fmt.Errorf("stage catalog: %w", err)
	}

	configPath, err := renderPackageConfig(cfg)
	if err != nil {
		return fmt.Errorf("render package config: %w", err)
	}

	log("Building .deb with nfpm " + nfpmVersion)
	target := filepath.Join(cfg.outDir, fmt.Sprintf("bloud_%s_%s.deb", cfg.version, cfg.arch))
	pack := exec.Command("go", "run", "github.com/goreleaser/nfpm/v2/cmd/nfpm@"+nfpmVersion,
		"package", "--config", configPath, "--target", target)
	pack.Dir = cfg.root
	pack.Stdout = os.Stdout
	pack.Stderr = os.Stderr
	if err := pack.Run(); err != nil {
		return fmt.Errorf("nfpm: %w", err)
	}

	log("Wrote " + target)
	return nil
}

// renderPackageConfig expands packaging/nfpm.yaml.tmpl with the resolved
// version and architecture into dist/nfpm.yaml and returns the path to pass to
// nfpm (relative to the repo root, which is nfpm's working directory).
func renderPackageConfig(cfg packageConfig) (string, error) {
	raw, err := os.ReadFile(filepath.Join(cfg.root, "packaging", "nfpm.yaml.tmpl"))
	if err != nil {
		return "", err
	}
	tmpl, err := template.New("nfpm").Parse(string(raw))
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	data := struct{ Version, Arch string }{cfg.version, cfg.arch}
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	out := filepath.Join(cfg.outDir, "nfpm.yaml")
	if err := os.WriteFile(out, []byte(buf.String()), 0644); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(cfg.root, out)
	if err != nil || strings.HasPrefix(rel, "..") {
		return out, nil
	}
	return "./" + rel, nil
}

// stageCatalog copies the on-disk catalog (metadata.yaml, icons, and any
// runtime assets) into the payload, skipping Go sources (configurators are
// compiled into the host-agent binary) and editor cruft.
func stageCatalog(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0755)
		}
		if !d.IsDir() && skipCatalogFile(d.Name()) {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target)
	})
}

// skipCatalogFile drops files the runtime never reads from the catalog dir:
// Go sources and their module files, Markdown docs, and dotfiles such as
// .DS_Store.
func skipCatalogFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch filepath.Ext(name) {
	case ".go", ".md":
		return true
	}
	return name == "go.mod" || name == "go.sum"
}

// copyTree recursively copies a directory tree, skipping dotfiles and the
// generated Go source the frontend build leaves in its output.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		name := d.Name()
		if !d.IsDir() && (strings.HasPrefix(name, ".") || filepath.Ext(name) == ".go") {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}

// packageVersion derives a Debian version from git describe, ensuring it
// starts with a digit (a hard requirement of the format).
func packageVersion(root string) string {
	v := ""
	if out, err := exec.Command("git", "-C", root, "describe", "--tags", "--always", "--dirty").Output(); err == nil {
		v = strings.TrimSpace(string(out))
	}
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return "0.1.0"
	}
	if v[0] < '0' || v[0] > '9' {
		return "0.0.0+" + v
	}
	return v
}
