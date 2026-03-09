# Read a metadata.yaml file and return native systemd service deps.
#
# Reads integrations.*.compatible[].app entries and maps each to "{app}.service".
# Native apps (postgres, redis) expose canonical {appName}.service aliases; other
# app names resolve to non-existent services and are silently ignored by systemd.
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
    map (compat: "${compat.app}.service") (int.compatible or [])
  ) (metadata.integrations or {})
))
