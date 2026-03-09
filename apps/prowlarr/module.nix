{ config, pkgs, lib, ... }:

let
  mkPodmanApp = import ../../nixos/lib/podman-app.nix { inherit config pkgs lib; };
in
mkPodmanApp {
  name = "prowlarr";
  description = "Indexer manager for Radarr, Sonarr, and other *arr apps";

  image = "linuxserver/prowlarr:latest";
  port = 9696;

  environment = cfg: {
    PUID = "1000";
    PGID = "1000";
    TZ = "Etc/UTC";
  };

  volumes = cfg: [
    "${cfg.appDataPath}/config:/config:z"
  ];

  dataDir = false;
  metadataFile = ./metadata.yaml;
}
