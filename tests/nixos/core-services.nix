# NixOS Integration Test: Core Services
#
# Tests that core Bloud infrastructure starts correctly:
# - PostgreSQL database
# - Redis cache
# - Traefik reverse proxy
#
# Run with: nix flake check
# Or: nix build .#checks.<system>.core-services
#
{ pkgs ? import <nixpkgs> {} }:

pkgs.nixosTest {
  name = "bloud-core-services";

  nodes.machine = { config, pkgs, lib, ... }: {
    imports = [
      ../../nixos/bloud.nix
    ];

    # Configure bloud
    bloud = {
      enable = true;
      user = "bloud";
    };

    # Enable core services
    bloud.apps.postgres.enable = true;
    bloud.apps.redis.enable = true;
    bloud.apps.traefik.enable = true;

    # Create test user with proper permissions
    users.users.bloud = {
      isNormalUser = true;
      uid = 1000;
      group = "users";
      extraGroups = [ "wheel" ];
      subUidRanges = [{ startUid = 100000; count = 65536; }];
      subGidRanges = [{ startGid = 100000; count = 65536; }];
    };

    # VM-specific config
    virtualisation = {
      memorySize = 2048;
      diskSize = 4096;
    };

    # Allow tests to run passwordless sudo
    security.sudo.wheelNeedsPassword = false;
  };

  testScript = ''
    import time

    # Wait for system to fully boot
    machine.wait_for_unit("multi-user.target")

    # Wait for user session to be ready
    machine.wait_for_unit("user@1000.service")

    # Give lingering time to initialize
    time.sleep(2)

    # Start the podman network first (dependency for services)
    machine.succeed("sudo -u bloud systemctl --user start podman-apps-network.service")
    machine.wait_until_succeeds("sudo -u bloud systemctl --user is-active podman-apps-network.service", timeout=30)

    # Start PostgreSQL and verify it responds
    machine.succeed("sudo -u bloud systemctl --user start podman-apps-postgres.service")
    machine.wait_until_succeeds(
      "sudo -u bloud podman exec apps-postgres pg_isready -U apps",
      timeout=60
    )

    # Verify PostgreSQL can handle queries
    machine.succeed(
      "sudo -u bloud podman exec apps-postgres psql -U apps -d apps -c 'SELECT 1'"
    )

    # Start Redis and verify it responds
    machine.succeed("sudo -u bloud systemctl --user start podman-apps-redis.service")
    machine.wait_until_succeeds(
      "sudo -u bloud podman exec apps-redis redis-cli ping | grep -q PONG",
      timeout=30
    )

    # Verify Redis can store and retrieve data
    machine.succeed(
      "sudo -u bloud podman exec apps-redis redis-cli SET test_key test_value"
    )
    machine.succeed(
      "sudo -u bloud podman exec apps-redis redis-cli GET test_key | grep -q test_value"
    )

    # Start Traefik and verify dashboard is accessible
    machine.succeed("sudo -u bloud systemctl --user start podman-traefik.service")
    machine.wait_until_succeeds(
      "curl -sf http://localhost:8080/api/overview",
      timeout=30
    )

    # Verify all services are running
    machine.succeed("sudo -u bloud systemctl --user is-active podman-apps-postgres.service")
    machine.succeed("sudo -u bloud systemctl --user is-active podman-apps-redis.service")
    machine.succeed("sudo -u bloud systemctl --user is-active podman-traefik.service")

    # List running containers (informational)
    machine.succeed("sudo -u bloud podman ps")
  '';
}
