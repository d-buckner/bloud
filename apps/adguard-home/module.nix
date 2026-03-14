{ config, pkgs, lib, ... }:

let
  mkNativeApp = import ../../nixos/lib/native-app.nix { inherit config pkgs lib; };
in
mkNativeApp {
  name = "adguard-home";
  description = "AdGuard Home DNS server";
  port = 3080;
  serviceName = "adguardhome";
  # No runtime integrations — no configurator hooks needed
  configuratorHooks = false;

  nixosConfig = cfg: {
    services.adguardhome = {
      enable = true;
      host = "0.0.0.0";
      port = cfg.port;
      # Allow runtime UI changes to persist across rebuilds
      mutableSettings = true;
      # Manage firewall ports ourselves (conditional on app enable)
      openFirewall = false;
      settings = {
        dns = {
          bind_hosts = [ "0.0.0.0" ];
          port = 53;
          upstream_dns = [ "https://dns10.quad9.net/dns-query" ];
          bootstrap_dns = [
            "9.9.9.10"
            "149.112.112.10"
          ];
          upstream_mode = "load_balance";
          cache_enabled = true;
          hostsfile_enabled = true;
        };
        filtering = {
          protection_enabled = true;
          filtering_enabled = true;
          filters_update_interval = 24;
        };
        filters = [
          {
            enabled = true;
            url = "https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt";
            name = "AdGuard DNS filter";
            id = 1;
          }
        ];
        querylog = {
          enabled = true;
          interval = "2160h";
        };
        statistics = {
          enabled = true;
          interval = "24h";
        };
      };
    };

    # Open DNS ports when AdGuard Home is enabled
    networking.firewall = {
      allowedTCPPorts = [ 53 ];
      allowedUDPPorts = [ 53 ];
    };
  };
}
