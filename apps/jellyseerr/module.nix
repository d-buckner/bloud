{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
in
mkBloudApp {
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
