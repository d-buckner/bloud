{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
in
mkBloudApp {
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
}
