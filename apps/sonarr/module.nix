{ config, pkgs, lib, ... }:

let
  mkPodmanApp = import ../../nixos/lib/podman-app.nix { inherit config pkgs lib; };
in
mkPodmanApp {
  name = "sonarr";
  description = "TV series collection manager and downloader";

  image = "linuxserver/sonarr:latest";
  port = 8989;

  environment = cfg: {
    PUID = "1000";
    PGID = "1000";
    TZ = "Etc/UTC";
  };

  # Volumes:
  # - /config: Sonarr's internal config (app-specific)
  # - /downloads: Shared downloads folder with qBittorrent
  # - /tv: Shared shows folder with Jellyfin (maps to media/shows)
  volumes = cfg: [
    "${cfg.appDataPath}/config:/config:z"
    "${cfg.configPath}/downloads:/downloads:z"
    "${cfg.configPath}/media/shows:/tv:z"
  ];

  dataDir = false;
}
