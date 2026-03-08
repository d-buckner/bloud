{ config, pkgs, lib, ... }:

let
  mkPodmanApp = import ../../nixos/lib/podman-app.nix { inherit config pkgs lib; };
in
mkPodmanApp {
  name = "redis";
  description = "Redis in-memory data store";
  containerName = "apps-redis";
  # serviceName should match containerName for consistent dependency resolution
  serviceName = "apps-redis";

  image = "redis:alpine";
  port = 6379;

  cmd = [ "--save" "60" "1" "--loglevel" "warning" ];

  dataDir = "/data";

  extraConfig = _: {
    bloud.pullImages = [ "redis:alpine" ];
  };
}
