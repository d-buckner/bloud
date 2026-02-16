{ pkgs, frontend }:

let
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
      && baseName != "web"  # Frontend is built separately
      && baseName != "build";
  };
in
pkgs.buildGo124Module {
  pname = "bloud-host-agent";
  version = "0.1.0";

  inherit src;

  # Build from the host-agent directory, but keep the full source tree
  # so the `replace ../../apps` directive in go.mod resolves correctly
  sourceRoot = "bloud-source/services/host-agent";

  subPackages = [ "cmd/host-agent" ];

  vendorHash = "sha256-AZMr4gpXHbLYCezFELHkB2AlVXaJkFMuzFxec2n02EY=";

  # The apps/go.mod has a reverse `replace` back to host-agent, creating a
  # circular module reference. Remove it before go mod download and go build.
  # postPatch runs in both the goModules derivation and the main build.
  postPatch = ''
    chmod -R u+w ../../apps
    sed -i '/replace codeberg.org\/d-buckner\/bloud-v3\/services\/host-agent/d' ../../apps/go.mod
  '';

  # Bundle the pre-built frontend and source tree assets
  postInstall = ''
    # Frontend SPA build
    mkdir -p $out/share/bloud/web/build
    cp -r ${frontend}/* $out/share/bloud/web/build/

    # App metadata and icons (needed at runtime for catalog API)
    mkdir -p $out/share/bloud/apps
    for app in ../../apps/*/; do
      appName=$(basename "$app")
      if [ -f "$app/metadata.yaml" ]; then
        mkdir -p "$out/share/bloud/apps/$appName"
        cp "$app/metadata.yaml" "$out/share/bloud/apps/$appName/"
        [ -f "$app/icon.png" ] && cp "$app/icon.png" "$out/share/bloud/apps/$appName/"
      fi
    done

    # NixOS modules and flake (for future nixos-rebuild support)
    mkdir -p $out/share/bloud/nixos
    cp -r ../../nixos/* $out/share/bloud/nixos/ 2>/dev/null || true
    cp ../../flake.nix $out/share/bloud/
    cp ../../flake.lock $out/share/bloud/
  '';

  meta = {
    description = "Bloud host agent - app management and web UI";
    mainProgram = "host-agent";
  };
}
