# Bloud Host Agent package
#
# Packages pre-built Go binary and frontend into a Nix derivation.
# The Go binary and frontend are built OUTSIDE the Nix sandbox by CI
# (or locally via scripts/build-iso.sh) using native Go/npm toolchains.
#
# This avoids vendorHash/npmDepsHash which break on every dependency change.
# Nix's sandbox blocks network access, requiring pre-declared hashes for any
# download. Those hashes are fragile. Since Go and npm builds are already
# reproducible (pinned by go.sum and package-lock.json), building inside the
# sandbox adds pain without meaningful benefit.
#
# Pre-built artifacts expected at:
#   build/host-agent   - Go binary (CGO_ENABLED=0 GOOS=linux GOARCH=amd64)
#   build/frontend/    - SvelteKit SPA build output

{ pkgs }:

let
  buildDir = ../../build;
  hasPrebuilt = builtins.pathExists (buildDir + "/host-agent")
    && builtins.pathExists (buildDir + "/frontend");

  src = builtins.path {
    path = ../..;
    name = "bloud-source";
    filter = path: type:
      let
        baseName = builtins.baseNameOf path;
      in
      baseName != ".git"
      && baseName != "node_modules"
      && baseName != "result"
      && baseName != "result-iso"
      && baseName != ".direnv"
      && baseName != "cli"
      && baseName != "lima"
      && baseName != "integration"
      && baseName != "web"
      && baseName != "build"
      && baseName != "services";
  };

  realPackage = pkgs.runCommand "bloud-host-agent-0.1.0" {} ''
    mkdir -p $out/bin
    cp ${buildDir + "/host-agent"} $out/bin/host-agent
    chmod +x $out/bin/host-agent

    # Frontend SPA build
    mkdir -p $out/share/bloud/web/build
    cp -r ${buildDir + "/frontend"}/* $out/share/bloud/web/build/

    # App metadata and icons (needed at runtime for catalog API)
    mkdir -p $out/share/bloud/apps
    for app in ${src}/apps/*/; do
      appName=$(basename "$app")
      if [ -f "$app/metadata.yaml" ]; then
        mkdir -p "$out/share/bloud/apps/$appName"
        cp "$app/metadata.yaml" "$out/share/bloud/apps/$appName/"
        [ -f "$app/icon.png" ] && cp "$app/icon.png" "$out/share/bloud/apps/$appName/"
      fi
    done

    # NixOS modules and flake (for future nixos-rebuild support)
    mkdir -p $out/share/bloud/nixos
    cp -r ${src}/nixos/* $out/share/bloud/nixos/ 2>/dev/null || true
    cp ${src}/flake.nix $out/share/bloud/
    cp ${src}/flake.lock $out/share/bloud/
  '';

  # Stub package: evaluates successfully but fails at build time.
  # Allows `nix flake check` to pass without pre-built artifacts.
  stubPackage = pkgs.runCommand "bloud-host-agent-stub-0.1.0" {} ''
    echo "ERROR: Pre-built artifacts not found in build/" >&2
    echo "Run: scripts/build-iso.sh (or see .github/workflows/build-iso.yml)" >&2
    exit 1
  '';

in
if hasPrebuilt then realPackage else stubPackage
