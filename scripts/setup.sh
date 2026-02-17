#!/bin/bash
set -e

echo "=== Claude Gateway Setup ==="
echo ""

# Check for .env
if [ ! -f .env ]; then
    echo "Creating .env from template..."
    cp .env.example .env
    echo "⚠️  Please edit .env with your credentials before continuing."
    echo "   Required: ANTHROPIC_API_KEY, TELEGRAM_BOT_TOKEN, ALLOWED_USER_IDS"
    echo "   Required: GOOGLE_OAUTH_CLIENT_ID, GOOGLE_OAUTH_CLIENT_SECRET"
    echo ""
    echo "Run this script again after editing .env."
    exit 1
fi

source .env

# Validate required vars
missing=()
[ -z "$ANTHROPIC_API_KEY" ] && missing+=("ANTHROPIC_API_KEY")
[ -z "$TELEGRAM_BOT_TOKEN" ] && missing+=("TELEGRAM_BOT_TOKEN")
[ -z "$ALLOWED_USER_IDS" ] && missing+=("ALLOWED_USER_IDS")
[ -z "$GOOGLE_OAUTH_CLIENT_ID" ] && missing+=("GOOGLE_OAUTH_CLIENT_ID")
[ -z "$GOOGLE_OAUTH_CLIENT_SECRET" ] && missing+=("GOOGLE_OAUTH_CLIENT_SECRET")

if [ ${#missing[@]} -gt 0 ]; then
    echo "❌ Missing required environment variables:"
    for var in "${missing[@]}"; do
        echo "   - $var"
    done
    echo ""
    echo "Edit .env and run this script again."
    exit 1
fi

echo "✅ Environment variables configured."

# Google OAuth token setup
if [ ! -d google-tokens ] || [ -z "$(ls -A google-tokens 2>/dev/null)" ]; then
    echo ""
    echo "=== Google OAuth Setup ==="
    echo "You need to authenticate with Google once (requires a browser)."
    echo ""
    echo "Option 1: If you have Python/pipx on this machine:"
    echo "  export GOOGLE_OAUTH_CLIENT_ID='$GOOGLE_OAUTH_CLIENT_ID'"
    echo "  export GOOGLE_OAUTH_CLIENT_SECRET='$GOOGLE_OAUTH_CLIENT_SECRET'"
    echo "  pipx install workspace-mcp"
    echo "  uvx workspace-mcp --tools gmail drive calendar sheets"
    echo "  # Complete the browser auth flow, then Ctrl+C"
    echo "  cp -r ~/.config/workspace-mcp ./google-tokens/"
    echo ""
    echo "Option 2: Copy tokens from another machine where you've already authenticated."
    echo ""

    read -p "Have you completed Google OAuth? (y/n) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Complete the OAuth flow above, then run this script again."
        exit 1
    fi
fi

echo "✅ Google tokens directory exists."

# Create project directory
mkdir -p my-project

echo ""
echo "=== Building and Starting ==="
echo ""

docker compose build
docker compose up -d

echo ""
echo "✅ Claude Gateway is running!"
echo ""
echo "Send a message to your Telegram bot to test."
echo "Use 'docker compose logs -f' to watch logs."
echo "Use 'docker compose down' to stop."
