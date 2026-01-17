# Redis - Bloud Integration

## Overview

Redis is a shared infrastructure service. One instance per Bloud host, used by apps requiring caching or message queuing (currently Authentik).

## Port & Network

- **Port:** 6379
- **Network:** `apps-net` (internal container network)
- **System App:** Yes (installed automatically when needed)

## Usage

Apps connect via the internal network:
```
redis://redis:6379
```

## Health Check

Uses `redis-cli ping` to verify the server is responsive.

## Dependencies

None - Redis is a leaf dependency in the infrastructure graph.

## Current Consumers

- **Authentik:** Uses Redis for caching and session storage
