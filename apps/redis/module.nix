{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
in
mkBloudApp {
  name = "redis";
  description = "Redis in-memory data store";
  containerName = "apps-redis";
  # serviceName should match containerName for consistent dependency resolution
  serviceName = "apps-redis";

  image = "redis:alpine";
  port = 6379;

  cmd = [ "--save" "60" "1" "--loglevel" "warning" ];

  dataDir = "/data";
}
