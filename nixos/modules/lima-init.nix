# Lima initialization module for NixOS
# Based on github:kasuboski/nixos-lima
#
# This module configures NixOS to work with Lima VM by:
# - Mounting cidata from Lima
# - Setting up users and SSH keys from Lima's cloud-init data
# - Running the Lima guest agent for port forwarding
# - Mounting 9p filesystems for host directory sharing

{ config, pkgs, lib, ... }:

let
  limaInitScript = pkgs.writeShellScript "lima-init" ''
    set -eux -o pipefail

    # Wait for cidata to be mounted (retry a few times)
    for i in {1..10}; do
      if [ -f /mnt/lima-cidata/lima.env ]; then
        break
      fi
      echo "Waiting for cidata... ($i/10)"
      sleep 1
    done

    if [ ! -f /mnt/lima-cidata/lima.env ]; then
      echo "Lima cidata not found, skipping lima-init"
      exit 0
    fi

    # Source Lima environment
    source /mnt/lima-cidata/lima.env

    # Create user if specified (or just update if exists)
    if [ -n "''${LIMA_CIDATA_USER:-}" ]; then
      # Get or create home directory
      if id "$LIMA_CIDATA_USER" &>/dev/null; then
        USER_HOME=$(getent passwd "$LIMA_CIDATA_USER" | cut -d: -f6)
      else
        # User doesn't exist, create it
        useradd -m -G wheel,users "$LIMA_CIDATA_USER" || true
        USER_HOME="/home/$LIMA_CIDATA_USER"
      fi

      # Set up SSH keys
      mkdir -p "$USER_HOME/.ssh"

      # Extract SSH keys from user-data
      if [ -f /mnt/lima-cidata/user-data ]; then
        ${pkgs.yq-go}/bin/yq '.ssh_authorized_keys[]' /mnt/lima-cidata/user-data 2>/dev/null | \
          tr -d '"' > "$USER_HOME/.ssh/authorized_keys" || true
      fi

      # Fix ownership (use numeric UID from getent if user exists)
      if id "$LIMA_CIDATA_USER" &>/dev/null; then
        USER_UID=$(id -u "$LIMA_CIDATA_USER")
        USER_GID=$(id -g "$LIMA_CIDATA_USER")
        chown -R "$USER_UID:$USER_GID" "$USER_HOME/.ssh" || true
      fi
      chmod 700 "$USER_HOME/.ssh" 2>/dev/null || true
      chmod 600 "$USER_HOME/.ssh/authorized_keys" 2>/dev/null || true

      echo "SSH keys configured for $LIMA_CIDATA_USER"
    fi

    # Mount 9p filesystems (Lima creates mount0, mount1, etc.)
    for tag in mount0 mount1 mount2 mount3; do
      if [ -e "/sys/bus/virtio/drivers/9pnet_virtio/*/mount_tag" ]; then
        for tagfile in /sys/bus/virtio/drivers/9pnet_virtio/*/mount_tag; do
          current_tag=$(cat "$tagfile" 2>/dev/null | tr -d '\0' || true)
          if [ "$current_tag" = "$tag" ]; then
            # Parse mount point from user-data mounts array
            idx=$(echo "$tag" | sed 's/mount//')
            mountpoint=$(${pkgs.yq-go}/bin/yq ".mounts[$idx][1]" /mnt/lima-cidata/user-data 2>/dev/null | tr -d '"' || true)
            if [ -n "$mountpoint" ] && [ "$mountpoint" != "null" ]; then
              mkdir -p "$mountpoint"
              if ! mountpoint -q "$mountpoint"; then
                mount -t 9p -o trans=virtio,version=9p2000.L,cache=mmap "$tag" "$mountpoint" && \
                  echo "Mounted $tag at $mountpoint" || true
              fi
            fi
          fi
        done
      fi
    done

    # Signal that we're ready
    touch /run/lima-boot-done
  '';
in
{
  # Mount cidata
  fileSystems."/mnt/lima-cidata" = {
    device = "/dev/disk/by-label/cidata";
    fsType = "iso9660";
    options = [ "ro" "nofail" ];
  };

  # Lima initialization service
  systemd.services.lima-init = {
    description = "Lima VM Initialization";
    wantedBy = [ "multi-user.target" ];
    after = [ "local-fs.target" ];
    before = [ "sshd.service" ];

    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = limaInitScript;
    };
  };

  # Lima guest agent for port forwarding
  # NOTE: Disabled because the nixpkgs 'lima' package doesn't include lima-guestagent
  # (that's the host-side package). Use SSH port forwarding instead: ./lima/dev ports
  # TODO: Build lima-guestagent from source if native port forwarding is needed
  # systemd.services.lima-guestagent = { ... };

  # Required packages
  environment.systemPackages = with pkgs; [
    bash
    sshfs
    fuse3
    git
    lima
    yq-go  # For parsing Lima's YAML user-data
  ];

  # Kernel parameters for Lima
  boot.kernel.sysctl = {
    "kernel.unprivileged_userns_clone" = 1;
    "net.ipv4.ping_group_range" = "0 2147483647";
  };

  # Enable NAT for networking
  networking.nat.enable = true;
}
