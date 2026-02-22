# Bloud Installer package
#
# Packages pre-built installer binary and static frontend into a Nix derivation.
# The Go binary and frontend are built OUTSIDE the Nix sandbox by CI using
# native Go/npm toolchains (same pattern as host-agent.nix).
#
# Pre-built artifacts expected at:
#   build/installer       - Go binary (CGO_ENABLED=0 GOOS=linux GOARCH=amd64)
#   build/installer-web/  - SvelteKit static adapter build output

{ pkgs }:

let
  buildDir = ../../build;
  hasPrebuilt = builtins.pathExists (buildDir + "/installer")
    && builtins.pathExists (buildDir + "/installer-web");

  realPackage = pkgs.runCommand "bloud-installer-0.1.0" {} ''
    mkdir -p $out/bin
    cp ${buildDir + "/installer"} $out/bin/bloud-installer
    chmod +x $out/bin/bloud-installer

    # Frontend SPA static build — served by the installer binary
    # The installer looks for web/build relative to WorkingDirectory
    mkdir -p $out/share/bloud-installer/web/build
    cp -r ${buildDir + "/installer-web"}/* $out/share/bloud-installer/web/build/
  '';

  stubPackage = pkgs.runCommand "bloud-installer-stub-0.1.0" {} ''
    echo "ERROR: Pre-built installer artifacts not found in build/" >&2
    echo "Build them first:" >&2
    echo "  cd services/installer && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../../build/installer ./cmd/installer" >&2
    echo "  npm run build --workspace=services/installer/web" >&2
    echo "  cp -r services/installer/web/build build/installer-web" >&2
    exit 1
  '';

in
if hasPrebuilt then realPackage else stubPackage
