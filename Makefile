.PHONY: setup build up down logs restart clean dev

# First-time setup
setup:
	bash scripts/setup.sh

# Build containers
build:
	docker compose build

# Start services
up:
	docker compose up -d

# Stop services
down:
	docker compose down

# View logs (follow)
logs:
	docker compose logs -f

# View gateway logs only
logs-gateway:
	docker compose logs -f gateway

# View sidecar logs only
logs-sidecar:
	docker compose logs -f sidecar

# Recreate containers (picks up .env / compose changes). `restart` alone
# reuses the existing container env and will keep a stale API key.
restart:
	docker compose up -d --force-recreate

# Restart sidecar only (useful if it crashes)
restart-sidecar:
	docker compose restart sidecar

# Build and run locally (Go gateway only, for development)
dev:
	go run ./cmd/gateway

# Clean up
clean:
	docker compose down -v --rmi local
