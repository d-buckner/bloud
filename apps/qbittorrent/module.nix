{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
in
mkBloudApp {
  name = "qbittorrent";
  description = "Feature-rich BitTorrent client with web UI";

  image = "linuxserver/qbittorrent:latest";
  port = 8086;
  containerPort = 8080;

  environment = cfg: {
    PUID = "1000";
    PGID = "1000";
    TZ = "Etc/UTC";
    WEBUI_PORT = "8080";
  };

  # App config stored in appDataPath, shared downloads folder
  dataDir = "/config";
  volumes = cfg: [
    "${cfg.configPath}/downloads:/downloads:z"
  ];

  # Preserve host UID so files are owned by bloud user, not remapped UID
  userns = "keep-id";
}
