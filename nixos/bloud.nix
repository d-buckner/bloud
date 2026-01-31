{ config, pkgs, lib, ... }:

# Bloud - Base Infrastructure Module (Rootless Podman)
#
# Provides core infrastructure for Bloud apps.
# App modules are automatically imported from ./apps/.
# Enable apps via generated apps.nix or manually with bloud.apps.<name>.enable = true.
#
# Usage in /etc/nixos/configuration.nix:
#
#   imports = [ /home/daniel/Projects/bloud-v3/nixos/bloud.nix ];
#
#   bloud.enable = true;
#

let
  cfg = config.bloud;

  # Auto-import all module.nix files from ../apps/<name>/ directories
  appsDir = ../apps;
  appDirs = builtins.attrNames (builtins.readDir appsDir);
  appModules = builtins.filter (p: builtins.pathExists p)
    (map (d: appsDir + "/${d}/module.nix") appDirs);
in
{
  imports = appModules;
  options.bloud = {
    enable = lib.mkEnableOption "Bloud infrastructure with rootless podman";

    user = lib.mkOption {
      type = lib.types.str;
      default = "daniel";
      description = "User to run bloud services as";
    };

    externalHost = lib.mkOption {
      type = lib.types.str;
      default = "http://localhost";
      description = "External host URL (scheme + hostname, no port). Used with traefik.port for app BASE_URLs.";
      example = "https://mybox.example.com";
    };

    authentikExternalHost = lib.mkOption {
      type = lib.types.str;
      default = "http://localhost";
      description = "External host URL for Authentik SSO (scheme + hostname, no port). Authentik is served at root-level paths (/application/, /flows/, etc.).";
      example = "https://mybox.example.com";
    };

    agentPath = lib.mkOption {
      type = lib.types.str;
      default = "/tmp/host-agent";
      description = "Path to the host-agent binary for app configuration hooks";
    };

    dataDir = lib.mkOption {
      type = lib.types.str;
      default = "bloud";
      description = "Name of the data directory under ~/.local/share/ (e.g., 'bloud' or 'bloud-test')";
    };
  };

  config = lib.mkIf cfg.enable {
    # Enable core infrastructure by default
    bloud.apps.postgres.enable = lib.mkDefault true;   # Shared database
    bloud.apps.redis.enable = lib.mkDefault true;      # Shared cache (used by Authentik)
    bloud.apps.traefik.enable = lib.mkDefault true;    # Routing
    bloud.apps.authentik.enable = lib.mkDefault true;  # Authentication/SSO

    # Create shared directories used by multiple apps
    # IMPORTANT: Also fix ownership of parent dirs (.local, .local/share) since
    # activation scripts run as root and may create these with root ownership
    system.activationScripts.bloud-shared-dirs = lib.stringAfter [ "users" ] ''
      mkdir -p /home/${cfg.user}/.local/share/${cfg.dataDir}/{downloads,media/{shows,movies}}
      chown ${cfg.user}:users /home/${cfg.user}/.local
      chown ${cfg.user}:users /home/${cfg.user}/.local/share
      chown ${cfg.user}:users /home/${cfg.user}/.local/share/${cfg.dataDir}
      chown ${cfg.user}:users /home/${cfg.user}/.local/share/${cfg.dataDir}/downloads
      chown -R ${cfg.user}:users /home/${cfg.user}/.local/share/${cfg.dataDir}/media
    '';

    # Enable rootless Podman
    virtualisation.podman = {
      enable = true;
      dockerCompat = false;
    };

    # Enable lingering for user so services start at boot
    system.activationScripts.enableLingering = ''
      ${pkgs.systemd}/bin/loginctl enable-linger ${cfg.user}
    '';

    # Target for all bloud apps - started by bloud-user-services AFTER network is ready
    # This target is NOT wanted by default.target to avoid race conditions
    systemd.user.targets.bloud-apps = {
      description = "Bloud Apps Target";
    };

    # Podman network creation (apps declare dependency on this via After=)
    # Note: Check if network exists first to avoid masking real errors with || true
    systemd.user.services.podman-apps-network = {
      description = "Create podman network for apps stack";
      wantedBy = [ "bloud-apps.target" ];
      before = [ "bloud-apps.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${pkgs.bash}/bin/bash -c '${pkgs.podman}/bin/podman network exists apps-net || ${pkgs.podman}/bin/podman network create apps-net'";
        ExecStop = "${pkgs.bash}/bin/bash -c '${pkgs.podman}/bin/podman network rm apps-net || true'";
      };
    };

    # Initialize the bloud database (required for app configurator hooks)
    # Apps with configurators should add this to their After= dependencies
    systemd.user.services.bloud-db-init = {
      description = "Initialize bloud database for host-agent";
      after = [ "podman-apps-postgres.service" ];
      requires = [ "podman-apps-postgres.service" ];
      wantedBy = [ "bloud-apps.target" ];
      before = [ "bloud-apps.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = pkgs.writeShellScript "bloud-db-init" ''
          set -e

          # Wait for postgres to be ready (with timeout)
          echo "Waiting for PostgreSQL to be ready..."
          for i in $(seq 1 30); do
            if ${pkgs.podman}/bin/podman exec apps-postgres pg_isready -U ${config.bloud.apps.postgres.user} > /dev/null 2>&1; then
              echo "PostgreSQL is ready"
              break
            fi
            if [ $i -eq 30 ]; then
              echo "Timeout waiting for PostgreSQL"
              exit 1
            fi
            sleep 2
          done

          # Create database if not exists
          if ! ${pkgs.podman}/bin/podman exec apps-postgres psql -U ${config.bloud.apps.postgres.user} -tc "SELECT 1 FROM pg_database WHERE datname = 'bloud'" | grep -q 1; then
            echo "Creating bloud database..."
            ${pkgs.podman}/bin/podman exec apps-postgres psql -U ${config.bloud.apps.postgres.user} -c "CREATE DATABASE bloud"
            ${pkgs.podman}/bin/podman exec apps-postgres psql -U ${config.bloud.apps.postgres.user} -c "GRANT ALL PRIVILEGES ON DATABASE bloud TO ${config.bloud.apps.postgres.user}"
            echo "Database created successfully"
          else
            echo "Database bloud already exists"
          fi
        '';
      };
    };

    # Helper commands
    environment.systemPackages = [
      (pkgs.writeShellScriptBin "bloud-reset" ''
        echo "╔════════════════════════════════════════════════════════════╗"
        echo "║              Bloud Reset (Full State Wipe)                 ║"
        echo "╚════════════════════════════════════════════════════════════╝"
        echo ""

        # Parse args
        KEEP_SECRETS=true
        for arg in "$@"; do
          case $arg in
            --include-secrets)
              KEEP_SECRETS=false
              ;;
          esac
        done

        DATA_DIR="$HOME/.local/share/${cfg.dataDir}"

        echo "This will:"
        echo "  • Stop all Bloud services"
        echo "  • Remove all containers and networks"
        echo "  • Delete all app data in $DATA_DIR"
        if [ "$KEEP_SECRETS" = "false" ]; then
          echo "  • Delete secrets.json and env files (--include-secrets)"
        else
          echo "  • Keep secrets.json (use --include-secrets to also remove)"
        fi
        echo ""

        read -p "Are you sure? [y/N] " confirm
        if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
          echo "Aborted."
          exit 1
        fi

        echo ""
        echo "Stopping services..."
        systemctl --user stop bloud-apps.target 2>/dev/null || true
        systemctl --user stop podman-apps-network.service 2>/dev/null || true

        echo "Removing containers..."
        ${pkgs.podman}/bin/podman stop -a 2>/dev/null || true
        ${pkgs.podman}/bin/podman rm -a -f 2>/dev/null || true

        echo "Removing networks..."
        ${pkgs.podman}/bin/podman network rm apps-net 2>/dev/null || true

        echo "Removing app data..."
        if [ -d "$DATA_DIR" ]; then
          if [ "$KEEP_SECRETS" = "true" ]; then
            for item in "$DATA_DIR"/*; do
              [ -e "$item" ] || continue
              name=$(basename "$item")
              case "$name" in
                secrets.json|*.env)
                  echo "  Keeping: $name"
                  ;;
                *)
                  echo "  Removing: $name"
                  ${pkgs.podman}/bin/podman unshare rm -rf "$item" 2>/dev/null || rm -rf "$item" 2>/dev/null || true
                  ;;
              esac
            done
          else
            echo "  Removing: $DATA_DIR"
            ${pkgs.podman}/bin/podman unshare rm -rf "$DATA_DIR" 2>/dev/null || rm -rf "$DATA_DIR" 2>/dev/null || true
          fi
        fi

        echo "Resetting systemd state..."
        systemctl --user reset-failed 2>/dev/null || true

        echo ""
        echo "════════════════════════════════════════════════════════════"
        echo "Reset complete. To restart with fresh state:"
        echo ""
        echo "  cd ~/bloud/services/host-agent && go build -o /tmp/host-agent ./cmd/host-agent"
        echo "  sudo nixos-rebuild switch --flake ~/bloud#dev-server --impure"
        echo "  sudo systemctl restart bloud-user-services"
        echo "════════════════════════════════════════════════════════════"
      '')
      (pkgs.writeShellScriptBin "bloud-test" ''
        echo "╔════════════════════════════════════════════════════════════╗"
        echo "║           Bloud Local Testing (Rootless)                  ║"
        echo "╚════════════════════════════════════════════════════════════╝"
        echo ""
        echo "Services (via Traefik on port 8080):"
        echo "  • Dashboard:      http://localhost:8080/dashboard/"
        echo "  • Actual Budget:  http://localhost:8080/embed/actual-budget/"
        echo "  • Miniflux:       http://localhost:8080/embed/miniflux/ (admin/admin123)"
        echo "  • Authentik:      http://localhost:9001 (akadmin/password)"
        echo ""
        echo "Commands:"
        echo "  bloud-test-integration     - Run integration tests"
        echo "  systemctl --user status podman-*    - View container status"
        echo "  podman ps                            - List running containers"
        echo "  podman logs <container>              - View logs"
        echo "  podman exec apps-postgres psql -U apps  - Access PostgreSQL"
        echo ""
      '')
      (pkgs.writeShellScriptBin "bloud-test-integration" ''
        echo "╔════════════════════════════════════════════════════════════╗"
        echo "║         Bloud Integration Tests                           ║"
        echo "╚════════════════════════════════════════════════════════════╝"
        echo ""

        FAILED=0
        PASSED=0

        test_service() {
          local name="$1"
          local url="$2"
          local expected_code="$3"
          local expected_pattern="$4"

          echo -n "Testing $name... "

          response=$(${pkgs.curl}/bin/curl -s -o /tmp/test-response -w "%{http_code}" "$url" 2>/dev/null || echo "000")

          if [ "$response" = "$expected_code" ]; then
            if [ -n "$expected_pattern" ]; then
              if grep -q "$expected_pattern" /tmp/test-response 2>/dev/null; then
                echo "✓ PASS"
                ((PASSED++))
              else
                echo "✗ FAIL (pattern not found)"
                ((FAILED++))
              fi
            else
              echo "✓ PASS"
              ((PASSED++))
            fi
          else
            echo "✗ FAIL (got HTTP $response, expected $expected_code)"
            ((FAILED++))
          fi
        }

        # Test container status
        echo "Checking containers..."
        RUNNING=$(${pkgs.podman}/bin/podman ps --format "{{.Names}}" 2>/dev/null | wc -l)
        echo "  Found $RUNNING running containers"
        echo ""

        # Test services
        echo "Testing service endpoints..."
        test_service "Traefik Dashboard" "http://localhost:8080/dashboard/" "200" "Traefik"
        test_service "Miniflux" "http://localhost:8080/embed/miniflux/" "200" "Miniflux"
        test_service "Actual Budget" "http://localhost:8080/embed/actual-budget/" "200" ""

        # Test database
        echo -n "Testing PostgreSQL... "
        if ${pkgs.podman}/bin/podman exec apps-postgres psql -U apps -d apps -c "SELECT 1" &>/dev/null; then
          echo "✓ PASS"
          ((PASSED++))
        else
          echo "✗ FAIL"
          ((FAILED++))
        fi

        # Test Authentik (only if running)
        if ${pkgs.podman}/bin/podman ps --format "{{.Names}}" 2>/dev/null | grep -q "apps-authentik-server"; then
          echo ""
          echo "Testing Authentik (detected running)..."
          test_service "Authentik" "http://localhost:9001/if/flow/initial-setup/" "302" ""
          test_service "Authentik API" "http://localhost:9001/api/v3/root/config/" "200" "error_reporting"
          echo -n "Testing Authentik OAuth2 provider... "
          if ${pkgs.curl}/bin/curl -s "http://localhost:9001/application/o/actual-budget/.well-known/openid-configuration" | grep -q "authorization_endpoint" 2>/dev/null; then
            echo "✓ PASS"
            ((PASSED++))
          else
            echo "✗ FAIL"
            ((FAILED++))
          fi
        else
          echo ""
          echo "Skipping Authentik tests (not running)"
        fi

        # Summary
        echo ""
        echo "════════════════════════════════════════════════════════════"
        echo "Results: $PASSED passed, $FAILED failed"
        echo "════════════════════════════════════════════════════════════"

        if [ $FAILED -gt 0 ]; then
          exit 1
        fi
      '')
    ];
  };
}
