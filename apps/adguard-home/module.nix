{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
in
mkBloudApp {
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
