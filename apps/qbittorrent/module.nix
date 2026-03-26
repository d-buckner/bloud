{ config, pkgs, lib, ... }:

let
  mkPodmanApp = import ../../nixos/lib/podman-app.nix { inherit config pkgs lib; appDir = ./.; };
  bloudCfg = config.bloud;
  configPath = "/home/${bloudCfg.user}/.local/share/${bloudCfg.dataDir}";
in
mkPodmanApp {
  name = "qbittorrent";
  description = "BitTorrent client with web UI";
  image = "lscr.io/linuxserver/qbittorrent:latest";
  port = 8086;
  containerPort = 8080;

  dataDir = "/config";

  environment = _: {
    PUID = "1000";
    PGID = "1000";
    TZ = "UTC";
    WEBUI_PORT = "8080";
  };

  volumes = _: [
    "${configPath}/downloads:/downloads:z"
  ];
}
