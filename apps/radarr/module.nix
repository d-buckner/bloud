{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
in
mkBloudApp {
  name = "radarr";
  description = "Movie collection manager and downloader";

  image = "linuxserver/radarr:latest";
  port = 7878;

  environment = cfg: {
    PUID = "1000";
    PGID = "1000";
    TZ = "Etc/UTC";
  };

  volumes = cfg: [
    "${cfg.appDataPath}/config:/config:z"
    "${cfg.configPath}/downloads:/downloads:z"
    "${cfg.configPath}/movies:/movies:z"
  ];

  dataDir = false;
}
