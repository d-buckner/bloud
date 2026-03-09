{ config, pkgs, lib, ... }:

let
  mkPodmanApp = import ../../nixos/lib/podman-app.nix { inherit config pkgs lib; appDir = ./.; };
in
mkPodmanApp {
  name = "adguard-home";
  description = "AdGuard Home DNS server";
  image = "adguard/adguardhome:latest";
  port = 3080;
  network = "host";

  cmd = [
    "--no-check-update"
    "--config" "/opt/adguardhome/conf/AdGuardHome.yaml"
    "--work-dir" "/opt/adguardhome/work"
    "--web-addr" "0.0.0.0:3080"
  ];

  volumes = cfg: [
    "${cfg.appDataPath}/work:/opt/adguardhome/work:Z"
    "${cfg.appDataPath}/conf:/opt/adguardhome/conf:Z"
  ];
}
