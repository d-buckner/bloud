# Read a metadata.yaml file and return native systemd service deps.
#
# Reads integrations.*.compatible[].app entries and maps each to "{app}.service".
# Native apps expose user-scope {name}.service aliases (via path units) so these
# can be used in both After= and Requires= of user-scope podman container services.
#
# Usage:
#   nativeDeps = import ../../nixos/lib/metadata.nix { inherit pkgs lib; };
#   nativeIntegrationDeps = nativeDeps ./metadata.yaml;  # e.g. ["postgres.service" "redis.service"]

{ pkgs, lib }:

metadataFile:

let
  metadataJsonDrv = pkgs.runCommand "metadata-json" { buildInputs = [ pkgs.yq-go ]; } ''
    yq -o=json ${metadataFile} > $out
  '';
  metadata = builtins.fromJSON (builtins.readFile metadataJsonDrv);
in
lib.unique (lib.flatten (
  lib.mapAttrsToList (_: int:
    # Only generate hard systemd deps for required integrations.
    # Optional integrations (e.g. sso) are handled by the configurator at runtime.
    if (int.required or false) then
      map (compat: "${compat.app}.service") (int.compatible or [])
    else
      []
  ) (metadata.integrations or {})
))
