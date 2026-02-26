# Docker Commands

## Quick Reference

```bash
# Build both containers
docker compose build

# Start both (detached)
docker compose up -d

# Stop both
docker compose down

# Full rebuild + restart
docker compose build && docker compose down && docker compose up -d

# Logs
docker compose logs -f              # all
docker compose logs -f gateway      # gateway only
docker compose logs -f sidecar      # sidecar only
```

## Build & Restart One Container

```bash
# Gateway only
docker compose build gateway && docker compose up -d gateway

# Sidecar only
docker compose build sidecar && docker compose up -d sidecar
```

## When to Rebuild vs Restart

| What changed | Action |
|---|---|
| `.env` | `docker compose down && docker compose up -d` |
| `docker-compose.yml` | `docker compose down && docker compose up -d` |
| `configs/gateway.yaml` | `docker compose restart gateway` |
| `skills/*.md` | Nothing — hot-reloaded automatically |
| `memory/bootstrap/*.md` | `docker compose restart gateway` |
| Go code (`internal/`, `cmd/`) | `docker compose build gateway && docker compose up -d gateway` |
| `Dockerfile.gateway` | `docker compose build gateway && docker compose up -d gateway` |
| `sidecar/index.js` | `docker compose build sidecar && docker compose up -d sidecar` |
| `sidecar/package.json` | `docker compose build sidecar && docker compose up -d sidecar` |
| `Dockerfile.sidecar` | `docker compose build sidecar && docker compose up -d sidecar` |

### Why

- **Restart** is enough when the code inside the container hasn't changed — only config or env vars that are read at startup.
- **Rebuild** is needed when the Docker image itself must change (new code, new dependencies, Dockerfile changes).
- **Down + Up** is needed when `docker-compose.yml` or `.env` changes, because Docker Compose needs to recreate the container with new volume mounts, env vars, or port mappings. A simple `restart` reuses the old container definition.
- **Skills** are watched by fsnotify and reloaded on file change — no restart needed.

## Debugging

```bash
# Shell into sidecar
docker compose exec sidecar bash

# Shell into gateway
docker compose exec gateway sh

# Check sidecar health
docker compose exec sidecar curl -s http://localhost:3001/health | python3 -m json.tool

# Check gog auth
docker compose exec sidecar gog auth status

# Test gog Gmail access
docker compose exec sidecar gog gmail search 'newer_than:1d' --max 5

# Check running containers
docker compose ps
```

## Makefile Shortcuts

```bash
make build      # docker compose build
make up         # docker compose up -d
make down       # docker compose down
make logs       # docker compose logs -f
make restart    # docker compose restart
make clean      # docker compose down -v --rmi local (removes volumes + images)
```
