{ config, pkgs, lib, ... }:

let
  mkPodmanApp = import ../../nixos/lib/podman-app.nix { inherit config pkgs lib; appDir = ./.; };
in
mkPodmanApp {
  name = "jellyseerr";
  description = "Media request and discovery tool for Jellyfin";

  image = "fallenbagel/jellyseerr:latest";
  port = 5055;

  environment = cfg: {
    TZ = "Etc/UTC";
  };

  volumes = cfg: [
    "${cfg.appDataPath}/config:/app/config:z"
  ];

  dataDir = false;
}
