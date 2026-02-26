# Claude Gateway v2

A self-hosted personal AI gateway that turns Claude into a persistent, context-aware assistant accessible through Telegram. It remembers every conversation, learns your preferences, monitors your inbox, automates your daily routine, and reviews your code — all running on your own infrastructure.

**Go + Node.js | PostgreSQL + pgvector | Ollama | Docker Compose**

## Quick Start

```bash
git clone <repo>
cd claude-gateway
cp .env.example .env   # edit with your credentials
make build && make up
```

## Documentation

- **[USER_GUIDE.md](USER_GUIDE.md)** — Full setup, configuration, commands, and troubleshooting
- **[PITCH.md](PITCH.md)** — Feature overview and architecture
- **[DOCKER.md](DOCKER.md)** — Container setup and deployment
- **[CLAUDE.md](CLAUDE.md)** — Development guidelines
