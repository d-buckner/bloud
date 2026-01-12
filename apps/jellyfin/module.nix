{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
in
mkBloudApp {
  name = "jellyfin";
  description = "Free software media system for streaming movies, TV, and music";

  image = "jellyfin/jellyfin:latest";
  port = 8096;

  environment = cfg: {
    PUID = "1000";
    PGID = "1000";
    TZ = "Etc/UTC";
  };

  volumes = cfg: [
    "${cfg.appDataPath}/config:/config:z"
    "${cfg.appDataPath}/cache:/cache:z"
    "${cfg.configPath}/movies:/movies:ro"
    "${cfg.configPath}/tv:/tv:ro"
  ];

  dataDir = false;
}
