{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
in
mkBloudApp {
  name = "sonarr";
  description = "TV series collection manager and downloader";

  image = "linuxserver/sonarr:latest";
  port = 8989;

  environment = cfg: {
    PUID = "1000";
    PGID = "1000";
    TZ = "Etc/UTC";
  };

  volumes = cfg: [
    "${cfg.appDataPath}/config:/config:z"
    "${cfg.configPath}/downloads:/downloads:z"
    "${cfg.configPath}/tv:/tv:z"
  ];

  dataDir = false;
}
