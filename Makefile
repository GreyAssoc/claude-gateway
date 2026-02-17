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

# Restart everything
restart:
	docker compose restart

# Restart sidecar only (useful if it crashes)
restart-sidecar:
	docker compose restart sidecar

# Build and run locally (Go gateway only, for development)
dev:
	go run ./cmd/gateway

# Clean up
clean:
	docker compose down -v --rmi local
