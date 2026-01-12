# qBittorrent Integration

## Overview

qBittorrent is a free, open-source BitTorrent client with a web-based UI. This integration uses the [linuxserver/qbittorrent](https://hub.docker.com/r/linuxserver/qbittorrent) image.

## Authentication

### With Authentik (SSO)

When Authentik is enabled, qBittorrent uses forward auth:
- Access via Traefik (`/embed/qbittorrent`) requires Authentik login
- Internal auth is bypassed for requests from localhost/private networks
- Direct port access (8086) still requires qBittorrent's internal auth as fallback

### Without Authentik

On first launch, the web UI requires authentication:
- **Username:** `admin`
- **Password:** Check container logs for the randomly generated password

```bash
./lima/dev shell "podman logs qbittorrent 2>&1 | grep -i password"
```

You should change this password immediately after first login via Settings > Web UI.

## Configuration

### Downloads Directory

By default, downloads are stored in `~/.local/share/bloud/qbittorrent/downloads`. To use a custom location:

```nix
bloud.apps.qbittorrent = {
  enable = true;
  downloadsPath = "/path/to/downloads";
};
```

### Port

The web UI runs on port 8086 by default. The container's internal port is 8080.

## Volume Mounts

- `/config` - qBittorrent configuration and database
- `/downloads` - Downloaded files

## Troubleshooting

### Container won't start

Check logs:
```bash
./lima/dev shell "journalctl --user -u podman-qbittorrent -n 50"
./lima/dev shell "podman logs qbittorrent"
```

### Permission issues

The container runs as PUID/PGID 1000. Ensure the data directories are owned correctly:
```bash
./lima/dev shell "ls -la ~/.local/share/bloud/qbittorrent/"
```

### Web UI not accessible

Verify the container is running and listening:
```bash
./lima/dev shell "podman ps | grep qbittorrent"
./lima/dev shell "curl -v http://localhost:8086/"
```

## Notes

- BitTorrent traffic uses the default ports configured in qBittorrent (typically 6881)
- The web UI is embedded in Bloud at `/embed/qbittorrent`
- Configuration persists in the config volume between container restarts
