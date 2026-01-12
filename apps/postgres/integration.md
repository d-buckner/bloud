# PostgreSQL - Bloud Integration

## System App

Marked as `isSystem: true` - shared infrastructure for all apps.

## Network
- **Network:** `apps-net`
- **Container:** `apps-postgres`
- **Address:** `apps-postgres:5432`

## Data Storage
- **Location:** `~/.local/share/bloud/apps-postgres/`
- **Mount:** `/var/lib/postgresql/data`

## User Namespace Mapping

PostgreSQL runs as UID 70. Container uses:
```nix
userns = "keep-id:uid=70,gid=70";
```

Maps host user → container postgres user (UID 70).

## Default Credentials
```nix
user = "apps"
password = "testpass123"
database = "apps"
```

## Shared Resource Architecture

Single PostgreSQL instance per host. Apps connect via:
```
DATABASE_URL=postgres://apps:testpass123@apps-postgres:5432/dbname?sslmode=disable
```

Each app needing a database:
1. Has a db-init oneshot service
2. Sets `waitFor` for postgres readiness
3. Uses shared credentials

## Health Check

```nix
waitFor = [
  { container = "apps-postgres"; command = "pg_isready -U apps"; }
];
```

## Container Configuration

```nix
mkPodmanService {
  name = "apps-postgres";
  image = "postgres:16-alpine";
  environment = {
    POSTGRES_USER = "apps";
    POSTGRES_DB = "apps";
    POSTGRES_PASSWORD = "testpass123";
  };
  volumes = [ "${configPath}/apps-postgres:/var/lib/postgresql/data:Z" ];
  network = "apps-net";
  userns = "keep-id:uid=70,gid=70";
};
```

## Note: Authentik

Authentik uses its own `authentik-postgres` instance for isolation.
