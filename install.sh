#!/usr/bin/env bash
# Claude Gateway v2 — Interactive Installer
#
# Usage:
#   bash install.sh                  # Interactive mode
#   bash install.sh --non-interactive  # Read from environment variables
#
# For --non-interactive mode, set these env vars:
#   Required: TELEGRAM_BOT_TOKEN, ALLOWED_USER_IDS, WORK_DIR, CLAUDE_CONFIG_DIR
#   Required: OWNER_NAME, BOT_NAME
#   Optional: OWNER_ROLE, PERSONALITY_STYLE (professional|casual|drywit|custom)
#   Optional: PERSONALITY_CUSTOM (if style=custom)
#   Optional: VERBOSITY (terse|concise|detailed), TIMEZONE (default: UTC)
#   Optional: DASHBOARD_PORT (default: 8080)
#   Optional: GOOGLE_ENABLED (true|false), GOG_ACCOUNT, GOG_KEYRING_PASSWORD
#   Optional: MOUNT_SPEC (semicolon-separated "host:container:access" entries)

set -euo pipefail

# ──────────────────────────────────────────────────────────────
# Constants
# ──────────────────────────────────────────────────────────────

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NON_INTERACTIVE=false
IS_WSL=false

# State variables (populated by wizards)
OWNER_NAME="${OWNER_NAME:-}"
OWNER_ROLE="${OWNER_ROLE:-}"
BOT_NAME="${BOT_NAME:-}"
PERSONALITY_STYLE="${PERSONALITY_STYLE:-}"
PERSONALITY_CUSTOM="${PERSONALITY_CUSTOM:-}"
VERBOSITY="${VERBOSITY:-}"
TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-}"
ALLOWED_USER_IDS="${ALLOWED_USER_IDS:-}"
WORK_DIR="${WORK_DIR:-}"
CLAUDE_CONFIG_DIR="${CLAUDE_CONFIG_DIR:-}"
GOOGLE_ENABLED="${GOOGLE_ENABLED:-false}"
GOG_ACCOUNT="${GOG_ACCOUNT:-}"
GOG_KEYRING_PASSWORD="${GOG_KEYRING_PASSWORD:-}"
TIMEZONE="${TIMEZONE:-}"
DASHBOARD_PORT="${DASHBOARD_PORT:-8080}"
POSTGRES_PASSWORD=""
CRON_ENABLED=false

# Dynamic mount arrays
MOUNT_PATHS=()
MOUNT_CONTAINER_PATHS=()
MOUNT_ACCESS=()

# ──────────────────────────────────────────────────────────────
# Flag parsing
# ──────────────────────────────────────────────────────────────

for arg in "$@"; do
    case "$arg" in
        --non-interactive) NON_INTERACTIVE=true ;;
        --help|-h)
            echo "Usage: bash install.sh [--non-interactive]"
            echo ""
            echo "Interactive installer for Claude Gateway v2."
            echo "Run with --non-interactive and set environment variables for unattended install."
            exit 0
            ;;
    esac
done

# ──────────────────────────────────────────────────────────────
# Utility functions
# ──────────────────────────────────────────────────────────────

print_header() { echo -e "\n${CYAN}${BOLD}=== $1 ===${NC}\n"; }
print_success() { echo -e "  ${GREEN}[OK]${NC} $1"; }
print_warning() { echo -e "  ${YELLOW}[!]${NC} $1"; }
print_error() { echo -e "  ${RED}[ERROR]${NC} $1"; }
print_info() { echo -e "  $1"; }

# Prompt with a default value. Sets the named variable.
# Usage: prompt_default "Prompt text" "default" VARIABLE_NAME
prompt_default() {
    local prompt="$1" default="$2" var_name="$3"
    if $NON_INTERACTIVE; then
        local current_val="${!var_name:-$default}"
        eval "$var_name=\"$current_val\""
        return
    fi
    local input
    read -rp "  $prompt [$default]: " input
    eval "$var_name=\"${input:-$default}\""
}

# Yes/no prompt. Returns "y" or "n".
# Usage: result=$(prompt_yes_no "Do this?" "y")
prompt_yes_no() {
    local prompt="$1" default="$2"
    if $NON_INTERACTIVE; then
        echo "$default"
        return
    fi
    local yn
    read -rp "  $prompt (y/n) [$default]: " yn
    yn="${yn:-$default}"
    [[ "$yn" =~ ^[Yy] ]] && echo "y" || echo "n"
}

# Generate a 32-character alphanumeric password.
generate_password() {
    openssl rand -base64 32 2>/dev/null | tr -dc 'A-Za-z0-9' | head -c 32 || \
        tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 32
}

# Check if a command exists.
check_command() { command -v "$1" &>/dev/null; }

# Validate a port number (1-65535).
validate_port() {
    [[ "$1" =~ ^[0-9]+$ ]] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]
}

# Expand tilde in a path variable. In WSL2, also convert Windows paths.
expand_path() {
    local val="$1"
    # Convert Windows paths (C:\Users\...) to WSL paths (/mnt/c/Users/...)
    if $IS_WSL && [[ "$val" =~ ^[A-Za-z]:\\ ]]; then
        if check_command wslpath; then
            val="$(wslpath -u "$val")"
        else
            # Manual conversion: C:\foo\bar -> /mnt/c/foo/bar
            local drive="${val:0:1}"
            drive="${drive,,}" # lowercase
            val="/mnt/${drive}${val:2}"
            val="${val//\\//}"
        fi
    fi
    echo "${val/#\~/$HOME}"
}

# ──────────────────────────────────────────────────────────────
# OS detection
# ──────────────────────────────────────────────────────────────

# Check if running inside WSL2 (Windows Subsystem for Linux).
detect_wsl() {
    if grep -qi "microsoft\|WSL" /proc/version 2>/dev/null; then
        return 0
    fi
    if [[ -n "${WSL_DISTRO_NAME:-}" ]]; then
        return 0
    fi
    return 1
}

detect_os() {
    # Detect native Windows (Git Bash / MSYS2 / Cygwin) — not supported directly
    case "$(uname -s)" in
        MINGW*|MSYS*|CYGWIN*)
            echo "windows_native"
            return
            ;;
    esac

    # Check for WSL2 first — sets the global flag, then returns the underlying distro
    if detect_wsl; then
        IS_WSL=true
    fi

    if [[ -f /etc/os-release ]]; then
        # shellcheck source=/dev/null
        . /etc/os-release
        case "${ID:-}" in
            ubuntu|debian|pop|linuxmint) echo "debian" ;;
            fedora) echo "fedora" ;;
            rhel|centos|rocky|alma) echo "rhel" ;;
            arch|manjaro|endeavouros) echo "arch" ;;
            *) echo "unknown" ;;
        esac
    elif [[ "$(uname)" == "Darwin" ]]; then
        echo "macos"
    else
        echo "unknown"
    fi
}

# ──────────────────────────────────────────────────────────────
# Dependency installation
# ──────────────────────────────────────────────────────────────

install_docker() {
    local os="$1"
    print_info "Installing Docker..."
    case "$os" in
        debian)
            sudo apt-get update -qq
            sudo apt-get install -y -qq ca-certificates curl gnupg
            sudo install -m 0755 -d /etc/apt/keyrings
            curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg 2>/dev/null
            sudo chmod a+r /etc/apt/keyrings/docker.gpg
            # shellcheck source=/dev/null
            . /etc/os-release
            local repo_id="${ID}"
            [[ "$repo_id" == "pop" || "$repo_id" == "linuxmint" ]] && repo_id="ubuntu"
            echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/${repo_id} ${VERSION_CODENAME} stable" | \
                sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
            sudo apt-get update -qq
            sudo apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
            ;;
        fedora)
            sudo dnf -y -q install dnf-plugins-core
            sudo dnf config-manager --add-repo https://download.docker.com/linux/fedora/docker-ce.repo
            sudo dnf install -y -q docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
            ;;
        rhel)
            sudo yum install -y -q yum-utils
            sudo yum-config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
            sudo yum install -y -q docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
            ;;
        arch)
            sudo pacman -S --noconfirm --quiet docker docker-compose docker-buildx
            ;;
        macos)
            print_error "Please install Docker Desktop from https://docker.com/products/docker-desktop"
            print_info "Then re-run this script."
            exit 1
            ;;
    esac
    if ! $IS_WSL; then
        sudo systemctl enable --now docker 2>/dev/null || true
    fi
    sudo usermod -aG docker "$USER" 2>/dev/null || true
}

install_postgres() {
    local os="$1"
    print_info "Installing PostgreSQL..."
    case "$os" in
        debian)
            sudo apt-get update -qq
            sudo apt-get install -y -qq postgresql postgresql-contrib
            ;;
        fedora)
            sudo dnf install -y -q postgresql-server postgresql-contrib
            sudo postgresql-setup --initdb 2>/dev/null || true
            ;;
        rhel)
            sudo yum install -y -q postgresql-server postgresql-contrib
            sudo postgresql-setup --initdb 2>/dev/null || true
            ;;
        arch)
            sudo pacman -S --noconfirm --quiet postgresql
            sudo -u postgres initdb -D /var/lib/postgres/data 2>/dev/null || true
            ;;
        macos)
            brew install postgresql@16
            brew services start postgresql@16
            ;;
    esac
    if [[ "$os" != "macos" ]]; then
        if $IS_WSL; then
            sudo service postgresql start 2>/dev/null || \
                sudo systemctl start postgresql 2>/dev/null || true
        else
            sudo systemctl enable --now postgresql 2>/dev/null || true
        fi
    fi
}

install_pgvector() {
    local os="$1"
    print_info "Installing pgvector extension..."
    case "$os" in
        debian)
            # Try package first, fall back to source
            if apt-cache search pgvector 2>/dev/null | grep -q pgvector; then
                sudo apt-get install -y -qq postgresql-*-pgvector 2>/dev/null || install_pgvector_from_source "$os"
            else
                install_pgvector_from_source "$os"
            fi
            ;;
        arch)
            if check_command yay; then
                yay -S --noconfirm postgresql-pgvector 2>/dev/null || install_pgvector_from_source "$os"
            else
                install_pgvector_from_source "$os"
            fi
            ;;
        macos)
            brew install pgvector 2>/dev/null || install_pgvector_from_source "$os"
            ;;
        *)
            install_pgvector_from_source "$os"
            ;;
    esac
}

install_pgvector_from_source() {
    local os="$1"
    print_info "Building pgvector from source..."
    case "$os" in
        debian) sudo apt-get install -y -qq postgresql-server-dev-all build-essential git ;;
        fedora) sudo dnf install -y -q postgresql-devel gcc make git ;;
        rhel) sudo yum install -y -q postgresql-devel gcc make git ;;
        arch) sudo pacman -S --noconfirm --quiet base-devel git ;;
    esac
    local tmp_dir
    tmp_dir=$(mktemp -d)
    git clone --branch v0.8.0 --depth 1 https://github.com/pgvector/pgvector.git "$tmp_dir/pgvector" 2>/dev/null
    cd "$tmp_dir/pgvector"
    make -j"$(nproc 2>/dev/null || echo 2)" 2>/dev/null
    sudo make install 2>/dev/null
    cd "$SCRIPT_DIR"
    rm -rf "$tmp_dir"
}

install_ollama() {
    print_info "Installing Ollama..."
    curl -fsSL https://ollama.com/install.sh | sh
    # Ensure running
    if ! curl -sf http://localhost:11434/api/tags &>/dev/null; then
        sudo systemctl enable --now ollama 2>/dev/null || (ollama serve &>/dev/null &)
        sleep 3
    fi
}

pull_ollama_models() {
    print_info "Pulling nomic-embed-text (embeddings)..."
    ollama pull nomic-embed-text
    print_info "Pulling qwen2.5:7b (triage LLM)..."
    ollama pull qwen2.5:7b
}

# ──────────────────────────────────────────────────────────────
# Wizard functions
# ──────────────────────────────────────────────────────────────

wizard_personality() {
    print_header "Personality Setup"

    # Owner name
    if [[ -z "$OWNER_NAME" ]]; then
        prompt_default "Your first name" "" OWNER_NAME
        while [[ -z "$OWNER_NAME" ]]; do
            print_error "Name is required."
            prompt_default "Your first name" "" OWNER_NAME
        done
    fi
    print_info "Owner: $OWNER_NAME"

    # Owner role (optional)
    if [[ -z "$OWNER_ROLE" ]] && ! $NON_INTERACTIVE; then
        prompt_default "Your role or occupation (optional, Enter to skip)" "" OWNER_ROLE
    fi

    # Bot name
    if [[ -z "$BOT_NAME" ]]; then
        prompt_default "Bot name" "Claude" BOT_NAME
    fi
    print_info "Bot name: $BOT_NAME"

    # Personality style
    if [[ -z "$PERSONALITY_STYLE" ]]; then
        if $NON_INTERACTIVE; then
            PERSONALITY_STYLE="professional"
        else
            echo ""
            echo "  Choose a personality style for $BOT_NAME:"
            echo ""
            echo "    1) Professional PA  - Formal, efficient, no-nonsense"
            echo "    2) Casual & direct  - Friendly, uses contractions, relaxed"
            echo "    3) Dry wit          - Professional with understated humour, deadpan"
            echo "    4) Custom           - Describe your own"
            echo ""
            local choice
            read -rp "  Style [1-4, default 1]: " choice
            case "$choice" in
                2) PERSONALITY_STYLE="casual" ;;
                3) PERSONALITY_STYLE="drywit" ;;
                4)
                    PERSONALITY_STYLE="custom"
                    echo ""
                    read -rp "  Describe the personality: " PERSONALITY_CUSTOM
                    ;;
                *) PERSONALITY_STYLE="professional" ;;
            esac
        fi
    fi

    # Verbosity
    if [[ -z "$VERBOSITY" ]]; then
        if $NON_INTERACTIVE; then
            VERBOSITY="concise"
        else
            echo ""
            echo "  Verbosity level:"
            echo ""
            echo "    1) Terse    - One-liners, minimal detail"
            echo "    2) Concise  - Short paragraphs, bullet points (recommended)"
            echo "    3) Detailed - Full explanations, thorough responses"
            echo ""
            local choice
            read -rp "  Verbosity [1-3, default 2]: " choice
            case "$choice" in
                1) VERBOSITY="terse" ;;
                3) VERBOSITY="detailed" ;;
                *) VERBOSITY="concise" ;;
            esac
        fi
    fi

    print_success "Personality: $BOT_NAME ($PERSONALITY_STYLE, $VERBOSITY)"
}

wizard_telegram() {
    print_header "Telegram Setup"

    if [[ -z "$TELEGRAM_BOT_TOKEN" ]]; then
        if ! $NON_INTERACTIVE; then
            echo "  To create a Telegram bot:"
            echo ""
            echo "    1. Open Telegram and search for @BotFather"
            echo "    2. Send /newbot"
            echo "    3. Choose a name and username for your bot"
            echo "    4. BotFather will give you a token like: 123456789:ABCdefGHIjklMNO"
            echo ""
        fi
        read -rp "  Bot token: " TELEGRAM_BOT_TOKEN
        while [[ -z "$TELEGRAM_BOT_TOKEN" ]] || [[ ! "$TELEGRAM_BOT_TOKEN" =~ ^[0-9]+:.+$ ]]; do
            print_error "Invalid token format. Expected: 123456789:ABCdef..."
            read -rp "  Bot token: " TELEGRAM_BOT_TOKEN
        done
    fi

    if [[ -z "$ALLOWED_USER_IDS" ]]; then
        if ! $NON_INTERACTIVE; then
            echo ""
            echo "  To find your Telegram user ID:"
            echo ""
            echo "    1. Search for @userinfobot in Telegram"
            echo "    2. Send /start — it replies with your numeric ID"
            echo ""
        fi
        read -rp "  Allowed user IDs (comma-separated): " ALLOWED_USER_IDS
        while [[ -z "$ALLOWED_USER_IDS" ]] || [[ ! "$ALLOWED_USER_IDS" =~ ^[0-9]+(,[0-9]+)*$ ]]; do
            print_error "Enter numeric IDs, comma-separated (e.g. 123456789 or 123,456)"
            read -rp "  Allowed user IDs: " ALLOWED_USER_IDS
        done
    fi

    print_success "Telegram configured"
}

wizard_workspace() {
    print_header "Workspace Setup"

    echo "  Choose a directory for your workspace."
    echo "  The bot will have read/write access to this directory."
    if $IS_WSL; then
        echo ""
        echo "  Tip: You can use Windows paths (e.g. C:\\Users\\name\\workspace)"
        echo "  or Linux paths (e.g. /home/name/workspace). Linux paths are faster."
    fi
    echo ""

    if [[ -z "$WORK_DIR" ]]; then
        prompt_default "Workspace directory" "$HOME/workspace" WORK_DIR
    fi
    WORK_DIR="$(expand_path "$WORK_DIR")"

    if [[ ! -d "$WORK_DIR" ]]; then
        local create
        create=$(prompt_yes_no "Directory does not exist. Create it?" "y")
        if [[ "$create" == "y" ]]; then
            mkdir -p "$WORK_DIR"
            print_success "Created $WORK_DIR"
        else
            print_error "Workspace directory must exist. Exiting."
            exit 1
        fi
    fi

    print_success "Workspace: $WORK_DIR"
}

wizard_mounts() {
    print_header "Additional Directory Mounts (Optional)"

    echo "  You can mount additional host directories into the bot's container."
    echo "  These become paths the bot can access (read-only or read-write)."
    echo ""

    # Parse MOUNT_SPEC for non-interactive mode: "host:container:access;host:container:access"
    if $NON_INTERACTIVE && [[ -n "${MOUNT_SPEC:-}" ]]; then
        IFS=';' read -ra specs <<< "$MOUNT_SPEC"
        for spec in "${specs[@]}"; do
            IFS=':' read -r hp cp ac <<< "$spec"
            MOUNT_PATHS+=("$hp")
            MOUNT_CONTAINER_PATHS+=("$cp")
            MOUNT_ACCESS+=("${ac:-ro}")
        done
        print_success "${#MOUNT_PATHS[@]} mount(s) configured from MOUNT_SPEC"
        return
    fi

    while true; do
        local add
        add=$(prompt_yes_no "Add a directory mount?" "n")
        [[ "$add" != "y" ]] && break

        local host_path container_path access

        read -rp "  Host path: " host_path
        host_path="$(expand_path "$host_path")"
        if [[ ! -d "$host_path" ]]; then
            print_warning "Path does not exist: $host_path"
            local cont
            cont=$(prompt_yes_no "Add anyway?" "n")
            [[ "$cont" != "y" ]] && continue
        fi

        read -rp "  Container path (e.g. /data/myfiles): " container_path
        while [[ -z "$container_path" ]] || [[ ! "$container_path" =~ ^/ ]]; do
            print_error "Container path must be absolute (start with /)"
            read -rp "  Container path: " container_path
        done

        echo "  Access level:"
        echo "    1) Read-only (recommended for data you don't want modified)"
        echo "    2) Read-write"
        local ac_choice
        read -rp "  Access [1-2, default 1]: " ac_choice
        case "$ac_choice" in
            2) access="rw" ;;
            *) access="ro" ;;
        esac

        MOUNT_PATHS+=("$host_path")
        MOUNT_CONTAINER_PATHS+=("$container_path")
        MOUNT_ACCESS+=("$access")

        print_success "Mount: $host_path -> $container_path ($access)"
        echo ""
    done
}

wizard_claude_oauth() {
    print_header "Claude OAuth Setup"

    if ! $NON_INTERACTIVE; then
        echo "  Claude Gateway uses your Claude subscription via OAuth — no API key needed."
        echo ""
        echo "  Setup steps:"
        if $IS_WSL; then
            echo "    1. Inside this WSL2 terminal, install the Claude Code CLI:"
            echo "       curl -fsSL https://claude.ai/install.sh | bash"
            echo ""
            echo "    2. Run 'claude' and complete the browser login"
            echo "       (The browser will open in Windows automatically)"
            echo ""
            echo "    3. Your credentials are stored in ~/.claude/"
        else
            echo "    1. Install the Claude Code CLI:"
            echo "       curl -fsSL https://claude.ai/install.sh | bash"
            echo ""
            echo "    2. Run 'claude' in your terminal and complete the browser login"
            echo ""
            echo "    3. Your credentials are stored in ~/.claude/"
        fi
        echo "       The installer needs the path to this directory."
        echo ""
    fi

    if [[ -z "$CLAUDE_CONFIG_DIR" ]]; then
        prompt_default "Path to your .claude directory" "$HOME/.claude" CLAUDE_CONFIG_DIR
    fi
    CLAUDE_CONFIG_DIR="$(expand_path "$CLAUDE_CONFIG_DIR")"

    if ! $NON_INTERACTIVE; then
        while [[ ! -d "$CLAUDE_CONFIG_DIR" ]]; do
            print_error "Directory not found: $CLAUDE_CONFIG_DIR"
            print_info "Have you completed the Claude CLI login? (Run 'claude' first)"
            prompt_default "Path to your .claude directory" "$HOME/.claude" CLAUDE_CONFIG_DIR
            CLAUDE_CONFIG_DIR="$(expand_path "$CLAUDE_CONFIG_DIR")"
        done
    fi

    print_success "Claude config: $CLAUDE_CONFIG_DIR"
}

wizard_google() {
    print_header "Google Workspace Integration (Optional)"

    echo "  The bot can access Gmail, Calendar, Drive, and Sheets"
    echo "  using the gog CLI. This requires a Google account."
    echo ""

    if ! $NON_INTERACTIVE; then
        local enable
        enable=$(prompt_yes_no "Enable Google Workspace integration?" "n")
        if [[ "$enable" == "y" ]]; then
            GOOGLE_ENABLED=true
        else
            GOOGLE_ENABLED=false
        fi
    fi

    if [[ "$GOOGLE_ENABLED" == "true" ]]; then
        if [[ -z "$GOG_ACCOUNT" ]]; then
            read -rp "  Google account email: " GOG_ACCOUNT
            while [[ -z "$GOG_ACCOUNT" ]] || [[ ! "$GOG_ACCOUNT" =~ @.*\. ]]; do
                print_error "Enter a valid email address"
                read -rp "  Google account email: " GOG_ACCOUNT
            done
        fi

        if [[ -z "$GOG_KEYRING_PASSWORD" ]]; then
            echo ""
            echo "  Choose a password for the gog CLI keyring (stored locally):"
            read -rsp "  Keyring password: " GOG_KEYRING_PASSWORD
            echo ""
            while [[ -z "$GOG_KEYRING_PASSWORD" ]]; do
                print_error "Password is required"
                read -rsp "  Keyring password: " GOG_KEYRING_PASSWORD
                echo ""
            done
        fi

        CRON_ENABLED=true
        print_success "Google Workspace enabled for $GOG_ACCOUNT"
    else
        CRON_ENABLED=false
        print_info "Skipped. Cron jobs and watchers will be disabled."
    fi
}

wizard_timezone() {
    print_header "Settings"

    if [[ -z "$TIMEZONE" ]]; then
        local current_tz="UTC"
        # Try multiple methods to detect timezone
        if check_command timedatectl; then
            current_tz=$(timedatectl show --property=Timezone --value 2>/dev/null || echo "UTC")
        elif [[ -f /etc/timezone ]]; then
            current_tz=$(cat /etc/timezone 2>/dev/null || echo "UTC")
        elif [[ -L /etc/localtime ]]; then
            current_tz=$(readlink -f /etc/localtime | sed 's|.*/zoneinfo/||' 2>/dev/null || echo "UTC")
        fi
        prompt_default "Timezone" "$current_tz" TIMEZONE
    fi
    print_success "Timezone: $TIMEZONE"
}

wizard_dashboard() {
    prompt_default "Dashboard port" "8080" DASHBOARD_PORT
    while ! validate_port "$DASHBOARD_PORT"; do
        print_error "Invalid port number (1-65535)"
        prompt_default "Dashboard port" "8080" DASHBOARD_PORT
    done
    print_success "Dashboard: http://localhost:$DASHBOARD_PORT"
}

# ──────────────────────────────────────────────────────────────
# File generation functions
# ──────────────────────────────────────────────────────────────

generate_env() {
    local env_file="$SCRIPT_DIR/.env"

    cat > "$env_file" << EOF
# Claude Gateway v2 — generated by install.sh
# $(date -Iseconds)

# Telegram
TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
ALLOWED_USER_IDS=${ALLOWED_USER_IDS}

# Workspace
WORK_DIR=${WORK_DIR}

# Claude OAuth credentials
CLAUDE_CONFIG_DIR=${CLAUDE_CONFIG_DIR}

# Dashboard
DASHBOARD_PORT=${DASHBOARD_PORT}
DASHBOARD_TOKEN=

# PostgreSQL
POSTGRES_PASSWORD=${POSTGRES_PASSWORD}

# Ollama (host.docker.internal works on Docker Desktop, Linux, and WSL2)
OLLAMA_URL=http://host.docker.internal:11434
OLLAMA_EMBED_MODEL=nomic-embed-text
OLLAMA_LLM_MODEL=qwen2.5:7b
EOF

    if [[ "$GOOGLE_ENABLED" == "true" ]]; then
        cat >> "$env_file" << EOF

# Google Workspace (gog CLI)
GOG_KEYRING_PASSWORD=${GOG_KEYRING_PASSWORD}
EOF
    fi

    cat >> "$env_file" << 'EOF'

# Gemini API Key (optional — for audio transcription)
GEMINI_API_KEY=
EOF

    chmod 600 "$env_file"
    print_success "Generated .env"
}

generate_docker_compose() {
    local file="$SCRIPT_DIR/docker-compose.yml"

    # --- Gateway service (static) ---
    cat > "$file" << 'GATEWAY_TOP'
services:
  gateway:
    build:
      context: .
      dockerfile: Dockerfile.gateway
    env_file: .env
    environment:
GATEWAY_TOP

    cat >> "$file" << EOF
      - TZ=${TIMEZONE}
      - SIDECAR_URL=http://sidecar:3001
      - DB_PATH=/app/data/gateway.db
      - WORK_DIR=/workspace
      - POSTGRES_URL=postgresql://gateway:\${POSTGRES_PASSWORD}@host.docker.internal:5432/gateway?sslmode=disable
      - OLLAMA_URL=http://host.docker.internal:11434
      - OLLAMA_EMBED_MODEL=\${OLLAMA_EMBED_MODEL:-nomic-embed-text}
      - OLLAMA_LLM_MODEL=\${OLLAMA_LLM_MODEL:-qwen2.5:7b}
    extra_hosts:
      - "host.docker.internal:host-gateway"
    volumes:
      - gateway-data:/app/data
      - \${WORK_DIR}/.claude/skills:/app/skills:ro
      - ./memory:/app/memory
      - ./configs:/app/configs
      - \${WORK_DIR}/.claude/agents:/app/agents:ro
    ports:
      - "\${DASHBOARD_PORT:-8080}:8080"
    depends_on:
      sidecar:
        condition: service_healthy
    restart: unless-stopped
EOF

    # --- Sidecar service ---
    cat >> "$file" << 'SIDECAR_TOP'

  sidecar:
    build:
      context: .
      dockerfile: Dockerfile.sidecar
    environment:
      - SIDECAR_PORT=3001
      - WORK_DIR=/workspace
      - NODE_OPTIONS=--dns-result-order=ipv4first
SIDECAR_TOP

    # Conditional gog environment
    if [[ "$GOOGLE_ENABLED" == "true" ]]; then
        cat >> "$file" << EOF
      - GOG_ACCOUNT=${GOG_ACCOUNT}
      - GOG_KEYRING_BACKEND=file
      - GOG_KEYRING_PASSWORD=\${GOG_KEYRING_PASSWORD}
      - GEMINI_API_KEY=\${GEMINI_API_KEY}
EOF
    fi

    # Sidecar volumes header
    cat >> "$file" << 'EOF'
    volumes:
      # Host workspace
      - ${WORK_DIR}:/workspace
EOF

    # Custom directory mounts
    for i in "${!MOUNT_PATHS[@]}"; do
        echo "      # Custom: ${MOUNT_PATHS[$i]}" >> "$file"
        echo "      - ${MOUNT_PATHS[$i]}:${MOUNT_CONTAINER_PATHS[$i]}:${MOUNT_ACCESS[$i]}" >> "$file"
    done

    # Claude OAuth mount
    cat >> "$file" << 'EOF'
      # Claude OAuth credentials (read-only, copied to writable dir at startup)
      - ${CLAUDE_CONFIG_DIR}:/root/.claude-ro:ro
EOF

    # gog CLI mount (conditional)
    if [[ "$GOOGLE_ENABLED" == "true" ]]; then
        cat >> "$file" << 'EOF'
      # gog CLI config (read-only, copied to writable dir at startup)
      - ${HOME}/.config/gogcli:/root/.config/gogcli-ro:ro
EOF
    fi

    # SSH keys mount
    cat >> "$file" << 'EOF'
      # SSH keys (read-only, copied to writable /root/.ssh at startup)
      - ${HOME}/.ssh:/root/.ssh-ro:ro
      # CUPS printing (host printer access via socket)
      - /var/run/cups/cups.sock:/var/run/cups/cups.sock
EOF

    # Sidecar bottom
    cat >> "$file" << 'EOF'
    expose:
      - "3001"
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:3001/health"]
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 15s
    restart: unless-stopped

volumes:
  gateway-data:
EOF

    print_success "Generated docker-compose.yml"
}

generate_personality_md() {
    local file="$SCRIPT_DIR/memory/bootstrap/personality.md"
    mkdir -p "$(dirname "$file")"

    # Map personality style to traits
    local traits
    case "$PERSONALITY_STYLE" in
        professional)
            traits="- Professional, efficient, and composed
- Prioritises clarity and accuracy above all else
- Formal but not stiff — warm enough to be approachable
- No unnecessary chatter or filler phrases
- States facts plainly and organises information logically
- When something goes wrong, reports it clearly with recommended next steps
- Does not over-apologise or hedge unnecessarily"
            ;;
        casual)
            traits="- Friendly and approachable, like a capable colleague
- Uses contractions, conversational tone, and plain language
- Gets straight to the point without being blunt
- Happy to explain things in everyday terms
- Occasionally uses humour naturally — not forced
- Relaxed but still reliable and thorough when it counts"
            ;;
        drywit)
            traits="- Professional and competent, with understated dry wit
- Takes the work seriously but not itself
- Occasionally wry or sardonic — never forced, never at ${OWNER_NAME}'s expense
- Deadpan delivery preferred over obvious jokes
- Helpful first, funny second — humour is background texture, not the point
- Unflappable. If something goes wrong, states it calmly with a hint of resigned amusement
- Does not apologise excessively or hedge unnecessarily — just gets on with it"
            ;;
        custom)
            traits="- ${PERSONALITY_CUSTOM}"
            ;;
        *)
            traits="- Helpful and professional"
            ;;
    esac

    # Map verbosity to communication style
    local comm_style
    case "$VERBOSITY" in
        terse)
            comm_style='- **Terse by default.** One-liners where possible. Bullet points over paragraphs.
- No preamble, no pleasantries, no filler. If it can be said in 5 words, use 5 words.
- Written for Telegram on mobile — extremely compact.
- Use markdown sparingly: `code` for technical references, bullet lists only when needed.
- No "Great question!" or "I'\''d be happy to help!"'
            ;;
        concise)
            comm_style='- **Concise by default.** One-liner when a one-liner will do. Structured with bullets when it won'\''t.
- Written for Telegram on mobile — short paragraphs, no walls of text.
- Use markdown formatting sparingly: **bold** for emphasis, `code` for technical references, bullet lists for multiple items.
- Skip pleasantries unless it'\''s the start of a conversation. No "Great question!" or "I'\''d be happy to help!"
- When asked something you don'\''t know, say so plainly rather than padding with caveats.'
            ;;
        detailed)
            comm_style='- **Thorough by default.** Provides full explanations with context and reasoning.
- Structures complex topics with headers and bullet points.
- Explains the "why" behind recommendations, not just the "what".
- Still avoids unnecessary padding or filler — detail should be useful, not verbose.
- Use markdown: **bold** for emphasis, `code` for technical references, headers for sections.'
            ;;
        *)
            comm_style='- Concise and clear. Uses bullet points for multiple items.'
            ;;
    esac

    # Owner context line
    local owner_line="- You know ${OWNER_NAME} as your primary user"
    if [[ -n "$OWNER_ROLE" ]]; then
        owner_line+=", who works as a ${OWNER_ROLE}"
    fi

    cat > "$file" << EOF
# Identity

You are **${BOT_NAME}**, ${OWNER_NAME}'s personal AI assistant. You run on ${OWNER_NAME}'s own infrastructure via the Claude Gateway.

## Personality

${traits}

## Communication Style

${comm_style}

## Working Relationship

${owner_line}
- You're proactive — if you spot something relevant (a scheduling conflict, a missed detail), flag it
- You remember previous conversations and build on them rather than starting fresh each time
- When ${OWNER_NAME} gives terse instructions, interpret them in context rather than asking for clarification on every detail
- You have opinions when asked, but defer to ${OWNER_NAME}'s judgement on decisions
EOF

    print_success "Generated memory/bootstrap/personality.md"
}

generate_environment_md() {
    local file="$SCRIPT_DIR/memory/bootstrap/environment.md"
    mkdir -p "$(dirname "$file")"

    # Start the file
    cat > "$file" << 'EOF'
# Environment

You are running inside a Docker container as part of the Claude Gateway system, accessible via Telegram.

## Directory Layout

| Path | Access | Description |
|------|--------|-------------|
| `/workspace` | Read/Write | Main working directory. All projects, code, and output goes here. |
EOF

    # Custom mount entries in the table
    for i in "${!MOUNT_PATHS[@]}"; do
        local cp="${MOUNT_CONTAINER_PATHS[$i]}"
        local ac="${MOUNT_ACCESS[$i]}"
        local desc
        if [[ "$ac" == "ro" ]]; then
            desc="**Read-only**"
        else
            desc="Read/Write"
        fi
        echo "| \`${cp}\` | ${desc} | Mounted from host. |" >> "$file"
    done

    # Rules
    cat >> "$file" << 'EOF'

## Important Rules

- The `/workspace` directory is your writable workspace — all output, generated files, and task results go here.

## Updating Bootstrap Instructions

**You CAN directly edit bootstrap files!** They are located in the `memory/bootstrap/` directory of your gateway project.

**When the user asks you to update bootstrap:**
1. Use the **Read tool** to read the current bootstrap file
2. Use the **Edit tool** to make the changes requested
3. Confirm the update was successful

**Examples of when to update bootstrap:**
- Adding new workflow instructions or protocols
- Documenting new tools or commands
- Adding trigger words for skills or commands
- Correcting command syntax or examples

**NEVER say** "I cannot update bootstrap" — you have direct file access via Read/Edit tools!
EOF

    # Check for any read-only mounts
    local has_ro=false
    for ac in "${MOUNT_ACCESS[@]}"; do
        [[ "$ac" == "ro" ]] && has_ro=true && break
    done
    if $has_ro; then
        echo "- Read-only directories cannot be modified. Copy files into \`/workspace\` to work on them." >> "$file"
    fi

    # Google Workspace section (conditional)
    if [[ "$GOOGLE_ENABLED" == "true" ]]; then
        cat >> "$file" << EOF

## Google Workspace (gog CLI)

Use the **Bash tool** to run \`gog\` commands for Google Workspace access (Gmail, Calendar, Drive, Sheets). Auth is pre-configured for \`${GOG_ACCOUNT}\` — no setup needed, just run the commands.

### Gmail
\`\`\`bash
gog gmail search 'newer_than:1d'              # Emails from last 24h
gog gmail search 'is:unread newer_than:7d'    # Unread from last week
gog gmail search 'from:someone@example.com'   # From a specific sender
gog gmail search 'subject:invoice' --max 10   # Search by subject
gog gmail thread <thread_id>                  # Read full thread
gog gmail send --to a@b.com --subject "Hi" --body "Message"  # Send email
gog gmail send --to a@b.com --subject "Hi" --body "Message" --attach /path/to/file.pdf  # With attachment
gog gmail send --to a@b.com --subject "Hi" --body "Message" --attach file1.pdf --attach file2.docx  # Multiple
gog gmail labels list                         # List labels
\`\`\`

**BANNED Gmail commands — NEVER use these under any circumstances:**
- \`gog gmail batch delete\` — permanently deletes emails
- \`gog gmail labels delete\` — removes labels
- \`gog gmail filters delete\` — removes filters
- \`gog gmail drafts delete\` — deletes drafts
- Any command containing \`delete\` or \`trash\` in a \`gog gmail\` context

Archiving emails (via \`gog gmail batch modify\` to remove the INBOX label) is permitted.

These delete commands are also blocked at the permissions level and will be rejected.

### Calendar
\`\`\`bash
gog calendar list                             # Today's events
gog calendar list --from tomorrow --to +7d    # Next 7 days
gog calendar add --title "Meeting" --start "2026-03-01 14:00" --duration 1h
gog calendar conflicts --from today --to +7d  # Detect scheduling conflicts
\`\`\`

### Drive (read-only)
\`\`\`bash
gog drive ls                                  # Root files
gog drive ls --parent <folder_id>             # List folder contents
gog drive search 'name contains "report"'     # Search files
gog drive download <file_id> --out /workspace/tasks/file.pdf
\`\`\`

**Drive is read-only** — listing, searching, and downloading are permitted. Deleting, trashing, and moving files are blocked.

### Sheets (read-only)
\`\`\`bash
gog sheets get <spreadsheet_id>               # Sheet metadata
gog sheets read <spreadsheet_id> 'Sheet1!A1:Z100'  # Read cell range
\`\`\`

**Sheets are read-only.** Writing, updating, appending, clearing, deleting, and creating sheets are all blocked.

### Common Flags
- \`--json\` — JSON output (for parsing/scripting)
- \`--plain\` — Tab-separated output
- \`--max N\` — Limit results
EOF
    fi

    # Skill detection section
    cat >> "$file" << 'EOF'

## Skills

Skills are auto-loaded from the skills directory and injected into your context below (in `<skills>` tags). When the user sends a natural language message, detect whether it matches a skill and follow that skill's full workflow.

**Detection rules:**
- Match by intent, not exact phrasing. "produce notices for Redan" = "generate notices for the Redan project" = "notices for Redan"
- When a skill is detected, follow its workflow steps exactly — don't improvise or skip steps
- If unsure which skill applies, ask the user to clarify
- If no skill matches, handle the message normally
- The `<skill_index>` below lists all available skills with descriptions — use it for matching
- Each skill's full workflow is in the `<skill>` blocks — read the matched skill's content for instructions
EOF

    # Web browsing section (always present)
    cat >> "$file" << 'EOF'

## Web Browsing & Research

You have full internet access. Use these tools to find information online:

### Quick Lookups (preferred — fast and lightweight)
- **WebFetch** — Fetch and read any web page. Give it a URL and describe what to extract.
- **WebSearch** — Search the web for information. Returns titles, snippets, and URLs.

### Browser Automation (Playwright MCP — for interactive sites)
For JavaScript-heavy pages, login flows, or complex interactions, use Playwright tools:
- `mcp__playwright__browser_navigate` — Open a URL in the browser
- `mcp__playwright__browser_screenshot` — Capture what's on screen
- `mcp__playwright__browser_click` — Click elements on the page
- `mcp__playwright__browser_type` — Type text into input fields

**Always try WebFetch/WebSearch first** — they're faster and use less resources.

## Available Agents

You have access to specialized agents organized across multiple categories. Check the agent registry before creating your own implementation.

**Agent Registry:** `/workspace/.claude/agent-registry.md` — complete catalog of all available agents

### Key Agent Categories

- **Coding Teams** — Go, Frontend, Kotlin, MQL teams with consensus protocols and security review
- **Research & Analysis** — SEO, UI/UX, content creation
- **Design Phase** — Tiered agents (Security [VETO], Compliance [VETO], Performance, Error Handling)

**When to Use Agents:**
- Use the **Task tool** to launch agents for autonomous multi-step work
- Reference agent files directly for quick decisions aligned with their principles
- **Instead of improvising, check the agent registry first** — an agent likely exists for your task

## How You're Accessed

- **Telegram**: Users message you via the bot
- **Cron jobs**: Scheduled tasks run automatically (if enabled)
EOF

    if [[ "$GOOGLE_ENABLED" == "true" ]]; then
        echo "- **Watchers**: Gmail and Calendar are polled periodically" >> "$file"
    fi

    print_success "Generated memory/bootstrap/environment.md"
}

generate_gateway_yaml() {
    local file="$SCRIPT_DIR/configs/gateway.yaml"
    mkdir -p "$(dirname "$file")"

    local cron_enabled="false"
    local watcher_enabled="false"
    if [[ "$CRON_ENABLED" == true ]]; then
        cron_enabled="true"
        watcher_enabled="true"
    fi

    cat > "$file" << EOF
# Gateway configuration — generated by install.sh
# Cron schedules use standard cron syntax: min hour day month weekday

database:
  path: "data/gateway.db"

skills:
  dirs:
    - "skills"

memory:
  bootstrap_dir: "memory/bootstrap"
  profiles_dir: "memory/profiles"
  daily_dir: "memory/daily"
  max_curated_inject: 20
  max_daily_days: 7
  claude_md_paths:
    - "/workspace/CLAUDE.md"
    - "/workspace/projects/CLAUDE.md"

orchestrator:
  agent_dna_dir: "/app/agents"
  timeout_seconds: 3600

dashboard:
  enabled: true
  port: 8080

cron:
  morning_briefing:
    schedule: "0 7 * * 1-5"
    prompt: >
      Check my Gmail for urgent unread messages from the last 12 hours,
      check my calendar for today's events, and give me a concise morning
      briefing. Keep it short — bullet the top 3 priorities.
    enabled: ${cron_enabled}

  end_of_day:
    schedule: "0 18 * * 1-5"
    prompt: >
      Summarize what happened today: important emails received, calendar
      events completed, and any action items I should follow up on tomorrow.
    enabled: ${cron_enabled}

  weekly_review:
    schedule: "0 9 * * 1"
    prompt: >
      Check my Google Drive for files modified in the last week in my
      Projects folder. Summarize key changes and flag anything that
      needs attention this week.
    enabled: ${cron_enabled}

  memory_compaction:
    schedule: "0 0 * * *"
    prompt: "__INTERNAL_MEMORY_COMPACT__"
    enabled: true

watchers:
  gmail:
    enabled: ${watcher_enabled}
    interval_seconds: 120
    prompt: >
      Check Gmail for messages received after {since}. If there are none,
      reply with exactly "NONE". If there are new messages, list sender
      and subject for each, one per line.

  calendar:
    enabled: ${watcher_enabled}
    interval_seconds: 300
    prompt: >
      Check my calendar for events starting in the next 15 minutes.
      Reply with JSON array: [{"id":"...","title":"...","time":"...","location":"..."}].
      If none, reply with [].

heartbeat:
  enabled: true
  interval_seconds: 300
  max_failures: 3

sidecar:
  url: "\${SIDECAR_URL:-http://sidecar:3001}"
  timeout_seconds: 3600
  health_path: "/health"
  default_model: "claude-opus-4-5-20251101"

ratelimiter:
  rpm: \${RATE_LIMIT_RPM:-100}
  itpm: \${RATE_LIMIT_ITPM:-80000}
  otpm: \${RATE_LIMIT_OTPM:-16000}
  safety_margin: 0.90

postgres_url: "\${POSTGRES_URL}"

ollama_url: "\${OLLAMA_URL:-http://host.docker.internal:11434}"
ollama_embed_model: "nomic-embed-text"
ollama_llm_model: "qwen2.5:7b"

context:
  recent_turns: 20
  replay_messages: 30
  semantic_hits: 5
  min_similarity: 0.75

triage:
  enabled: true
  auto_learn: true
  max_auto_facts: 200
  min_importance: 4
EOF

    print_success "Generated configs/gateway.yaml"
}

generate_claude_code_settings() {
    local file="$SCRIPT_DIR/configs/claude-code-settings.json"
    mkdir -p "$(dirname "$file")"

    cat > "$file" << 'EOF'
{
  "permissions": {
    "deny": [
      "Bash(*gog gmail*delete*)",
      "Bash(*gog gmail*trash*)",
      "Bash(*gog gmail*batch delete*)",
      "Bash(*gog gmail*labels delete*)",
      "Bash(*gog gmail*filters delete*)",
      "Bash(*gog gmail*drafts delete*)",
      "Bash(*gog sheets*write*)",
      "Bash(*gog sheets*update*)",
      "Bash(*gog sheets*append*)",
      "Bash(*gog sheets*clear*)",
      "Bash(*gog sheets*delete*)",
      "Bash(*gog sheets*create*)",
      "Bash(*gog drive*delete*)",
      "Bash(*gog drive*trash*)",
      "Bash(*gog drive*move*)"
    ],
    "allow": [
      "Bash(*)",
      "Read(*)",
      "Write(*)",
      "Edit(*)",
      "WebFetch(*)",
      "WebSearch(*)",
      "mcp__playwright__*"
    ]
  },
  "mcpServers": {
    "playwright": {
      "command": "npx",
      "args": ["-y", "@playwright/mcp@latest", "--headless"]
    }
  }
}
EOF

    print_success "Generated configs/claude-code-settings.json"
}

# ──────────────────────────────────────────────────────────────
# Database setup
# ──────────────────────────────────────────────────────────────

setup_postgres_db() {
    print_header "PostgreSQL Database Setup"

    POSTGRES_PASSWORD=$(generate_password)

    # Check if PostgreSQL is reachable
    if ! sudo -u postgres psql -c "SELECT 1" &>/dev/null; then
        # WSL2 may need the service started first
        if $IS_WSL; then
            print_info "Starting PostgreSQL in WSL2..."
            sudo pg_ctlcluster "$(pg_lsclusters -h 2>/dev/null | head -1 | awk '{print $1}')" main start 2>/dev/null || \
                sudo service postgresql start 2>/dev/null || true
            sleep 2
        fi
        if ! sudo -u postgres psql -c "SELECT 1" &>/dev/null; then
            print_warning "Cannot connect to PostgreSQL as postgres user."
            print_info "You may need to configure pg_hba.conf for local peer authentication."
            if $IS_WSL; then
                print_info "In WSL2, try: sudo service postgresql start"
            fi
            print_info "Skipping automatic database setup. You can run db/init.sql manually."
            return
        fi
    fi

    # Create role if it doesn't exist
    if sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='gateway'" 2>/dev/null | grep -q 1; then
        print_warning "PostgreSQL role 'gateway' already exists"
        local reuse
        reuse=$(prompt_yes_no "Update password and continue?" "y")
        if [[ "$reuse" == "y" ]]; then
            sudo -u postgres psql -c "ALTER ROLE gateway WITH PASSWORD '${POSTGRES_PASSWORD}';" 2>/dev/null
        else
            print_info "Keeping existing role."
            read -rp "  Enter existing password for 'gateway' role: " POSTGRES_PASSWORD
        fi
    else
        sudo -u postgres psql -c "CREATE ROLE gateway WITH LOGIN PASSWORD '${POSTGRES_PASSWORD}';" 2>/dev/null
        print_success "Created role 'gateway'"
    fi

    # Create database if it doesn't exist
    if sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='gateway'" 2>/dev/null | grep -q 1; then
        print_warning "Database 'gateway' already exists"
    else
        sudo -u postgres psql -c "CREATE DATABASE gateway OWNER gateway;" 2>/dev/null
        print_success "Created database 'gateway'"
    fi

    # Grant permissions
    sudo -u postgres psql -d gateway -c "GRANT ALL ON ALL TABLES IN SCHEMA public TO gateway;" 2>/dev/null || true
    sudo -u postgres psql -d gateway -c "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO gateway;" 2>/dev/null || true

    # Run init.sql
    if [[ -f "$SCRIPT_DIR/db/init.sql" ]]; then
        sudo -u postgres psql -d gateway -f "$SCRIPT_DIR/db/init.sql" 2>/dev/null
        # Re-grant after init.sql creates new tables
        sudo -u postgres psql -d gateway -c "GRANT ALL ON ALL TABLES IN SCHEMA public TO gateway;" 2>/dev/null || true
        sudo -u postgres psql -d gateway -c "GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO gateway;" 2>/dev/null || true
        print_success "Database schema initialized"
    else
        print_warning "db/init.sql not found — schema must be applied manually"
    fi
}

# ──────────────────────────────────────────────────────────────
# Build & start
# ──────────────────────────────────────────────────────────────

build_images() {
    print_header "Building Docker Images"
    print_info "This may take a few minutes on first run..."
    cd "$SCRIPT_DIR"
    docker compose build
    print_success "Docker images built"
}

start_services() {
    print_header "Starting Services"
    cd "$SCRIPT_DIR"
    docker compose up -d
    print_success "Services started"

    echo ""
    print_info "Waiting for services to become healthy..."
    local attempts=0
    while [ $attempts -lt 12 ]; do
        sleep 5
        if docker compose ps 2>/dev/null | grep -q "healthy"; then
            print_success "All services are healthy"
            return
        fi
        attempts=$((attempts + 1))
    done
    print_warning "Services may still be starting. Check with: docker compose ps"
}

# ──────────────────────────────────────────────────────────────
# Main
# ──────────────────────────────────────────────────────────────

main() {
    echo ""
    echo -e "${CYAN}${BOLD}"
    echo "  ╔══════════════════════════════════════════════════════════╗"
    echo "  ║            Claude Gateway v2 — Installer                ║"
    echo "  ║                                                         ║"
    echo "  ║   Your AI assistant — always on, always learning        ║"
    echo "  ╚══════════════════════════════════════════════════════════╝"
    echo -e "${NC}"

    # --- System requirements ---
    print_header "System Requirements"
    local os
    os=$(detect_os)
    print_info "Detected OS: $os"

    if [[ "$os" == "windows_native" ]]; then
        print_error "Direct Windows installation is not supported."
        echo ""
        print_info "Claude Gateway requires WSL2 (Windows Subsystem for Linux)."
        echo ""
        echo "  Setup steps:"
        echo "    1. Open PowerShell as Administrator and run:"
        echo "       wsl --install -d Ubuntu"
        echo ""
        echo "    2. Restart your computer when prompted"
        echo ""
        echo "    3. Open Ubuntu from the Start menu and create your user"
        echo ""
        echo "    4. Inside the Ubuntu terminal, run this installer again:"
        echo "       bash install.sh"
        echo ""
        exit 1
    fi

    if [[ "$os" == "unknown" ]]; then
        print_error "Unsupported operating system."
        print_info "Supported: Ubuntu/Debian, Fedora/RHEL/CentOS, Arch/Manjaro, macOS, Windows 11 (via WSL2)"
        exit 1
    fi

    if $IS_WSL; then
        print_info "Running inside WSL2"
        print_info "Docker Desktop for Windows will be used for containers."
    fi

    # Check sudo access (needed for PostgreSQL, Ollama, and Docker on Linux)
    if [[ "$os" != "macos" ]] && ! sudo -v 2>/dev/null; then
        print_error "sudo access is required for installing dependencies."
        if $IS_WSL; then
            print_info "Try setting your password with: passwd"
        fi
        exit 1
    fi

    for cmd in curl git; do
        if check_command "$cmd"; then
            print_success "$cmd is installed"
        else
            print_error "$cmd is required but not installed."
            exit 1
        fi
    done

    # --- Docker ---
    print_header "Docker"
    if check_command docker; then
        print_success "Docker is installed"
        if $IS_WSL; then
            # Verify Docker Desktop integration is working
            if docker info &>/dev/null 2>&1; then
                print_success "Docker Desktop WSL2 integration is working"
            else
                print_error "Docker is found but not responding."
                echo ""
                echo "  Ensure Docker Desktop for Windows is running and WSL2 integration is enabled:"
                echo "    1. Open Docker Desktop"
                echo "    2. Settings → Resources → WSL Integration"
                echo "    3. Enable integration with your WSL2 distro"
                echo "    4. Apply & Restart"
                echo ""
                exit 1
            fi
        fi
    else
        if $IS_WSL; then
            print_error "Docker is not available in this WSL2 distro."
            echo ""
            echo "  Install Docker Desktop for Windows:"
            echo "    1. Download from https://docker.com/products/docker-desktop"
            echo "    2. Install and restart your computer"
            echo "    3. Open Docker Desktop → Settings → Resources → WSL Integration"
            echo "    4. Enable integration with your WSL2 distro"
            echo "    5. Apply & Restart, then re-run this installer"
            echo ""
            exit 1
        else
            print_warning "Docker is not installed"
            local install_it
            install_it=$(prompt_yes_no "Install Docker?" "y")
            if [[ "$install_it" == "y" ]]; then
                install_docker "$os"
                print_success "Docker installed"
                print_warning "You may need to log out and back in for Docker group membership to take effect."
            else
                print_error "Docker is required. Exiting."
                exit 1
            fi
        fi
    fi

    if docker compose version &>/dev/null; then
        print_success "Docker Compose v2 is available"
    else
        print_error "Docker Compose v2 plugin not found."
        if $IS_WSL; then
            print_info "Ensure Docker Desktop is updated to the latest version."
        else
            print_info "It should be bundled with Docker CE. Try reinstalling Docker."
        fi
        exit 1
    fi

    # --- PostgreSQL ---
    print_header "PostgreSQL"
    if check_command psql; then
        print_success "PostgreSQL is installed"
        if pg_isready &>/dev/null 2>&1 || systemctl is-active postgresql &>/dev/null 2>&1; then
            print_success "PostgreSQL is running"
        else
            print_warning "PostgreSQL is installed but not running"
            if $IS_WSL; then
                # WSL2 doesn't always have systemd; try direct pg_ctlcluster
                sudo pg_ctlcluster "$(pg_lsclusters -h 2>/dev/null | head -1 | awk '{print $1}')" main start 2>/dev/null || \
                    sudo service postgresql start 2>/dev/null || \
                    sudo systemctl start postgresql 2>/dev/null || true
            else
                sudo systemctl start postgresql 2>/dev/null || true
            fi
        fi
        # Check pgvector
        if sudo -u postgres psql -c "SELECT 1 FROM pg_available_extensions WHERE name='vector'" -tA 2>/dev/null | grep -q 1; then
            print_success "pgvector extension is available"
        else
            print_warning "pgvector extension not found"
            install_pgvector "$os"
            print_success "pgvector installed"
        fi
    else
        print_warning "PostgreSQL is not installed"
        local install_it
        install_it=$(prompt_yes_no "Install PostgreSQL with pgvector?" "y")
        if [[ "$install_it" == "y" ]]; then
            install_postgres "$os"
            install_pgvector "$os"
            print_success "PostgreSQL + pgvector installed"
        else
            print_error "PostgreSQL is required. Exiting."
            exit 1
        fi
    fi

    # --- Ollama ---
    print_header "Ollama"
    local ollama_available=false
    local ollama_url="http://localhost:11434"

    if $IS_WSL; then
        # In WSL2, Ollama might be running on the Windows host
        if curl -sf http://localhost:11434/api/tags &>/dev/null; then
            print_success "Ollama is responding on localhost (WSL2)"
            ollama_available=true
        else
            print_info "Ollama not found on localhost. Checking if installed in WSL2..."
        fi
    fi

    if ! $ollama_available && check_command ollama; then
        print_success "Ollama is installed"
        if curl -sf http://localhost:11434/api/tags &>/dev/null; then
            print_success "Ollama is running"
            ollama_available=true
        else
            print_warning "Ollama is installed but not responding"
            if $IS_WSL; then
                # WSL2 may not have systemd
                ollama serve &>/dev/null &
            else
                sudo systemctl start ollama 2>/dev/null || (ollama serve &>/dev/null &)
            fi
            sleep 3
            if curl -sf http://localhost:11434/api/tags &>/dev/null; then
                ollama_available=true
                print_success "Ollama started"
            fi
        fi
    fi

    if ! $ollama_available; then
        print_warning "Ollama is not installed"
        if $IS_WSL; then
            echo ""
            echo "  You can install Ollama either:"
            echo "    a) On Windows: download from https://ollama.com (runs as Windows service)"
            echo "    b) Inside WSL2: this installer can do it for you"
            echo ""
        fi
        local install_it
        install_it=$(prompt_yes_no "Install Ollama inside $(if $IS_WSL; then echo "WSL2"; else echo "this system"; fi)? (recommended for local LLM triage)" "y")
        if [[ "$install_it" == "y" ]]; then
            install_ollama
            ollama_available=true
            print_success "Ollama installed"
        else
            print_info "Skipped. Triage will pass all messages to Claude."
        fi
    fi

    if $ollama_available; then
        # Check and pull models
        local models
        models=$(ollama list 2>/dev/null || echo "")
        local need_pull=false
        if ! echo "$models" | grep -q "nomic-embed-text"; then need_pull=true; fi
        if ! echo "$models" | grep -q "qwen2.5:7b"; then need_pull=true; fi
        if $need_pull; then
            pull_ollama_models
            print_success "Ollama models ready"
        else
            print_success "Required Ollama models already present"
        fi
    fi

    # --- Wizards ---
    wizard_personality
    wizard_telegram
    wizard_workspace
    wizard_mounts
    wizard_claude_oauth
    wizard_google
    wizard_timezone
    wizard_dashboard

    # --- Database ---
    setup_postgres_db

    # --- Generate config files ---
    print_header "Generating Configuration"

    # Ensure directories exist
    mkdir -p "$SCRIPT_DIR/memory/bootstrap"
    mkdir -p "$SCRIPT_DIR/memory/daily"
    mkdir -p "$SCRIPT_DIR/memory/profiles"
    mkdir -p "$WORK_DIR/.claude/skills"
    mkdir -p "$SCRIPT_DIR/configs"

    generate_env
    generate_docker_compose
    generate_personality_md
    generate_environment_md
    generate_gateway_yaml
    generate_claude_code_settings

    # --- Build & start ---
    build_images
    start_services

    # --- Post-setup guidance ---
    if [[ "$GOOGLE_ENABLED" == "true" ]]; then
        print_header "Google Workspace Authentication"
        echo ""
        echo "  To complete Google Workspace setup, run:"
        echo ""
        echo "    docker compose exec sidecar gog auth login"
        echo ""
        echo "  Follow the browser prompts to authenticate."
        echo ""
    fi

    # --- Done ---
    print_header "Installation Complete!"
    echo ""
    echo -e "  ${GREEN}${BOLD}Claude Gateway v2 is running!${NC}"
    echo ""
    echo "  Next steps:"
    echo "    1. Open Telegram and find your bot"
    echo "    2. Send /start to begin chatting with ${BOT_NAME}"
    echo ""
    echo "  Useful commands:"
    echo "    docker compose logs -f          # Watch all logs"
    echo "    docker compose logs -f gateway  # Gateway logs only"
    echo "    docker compose logs -f sidecar  # Sidecar logs only"
    echo "    docker compose down             # Stop services"
    echo "    docker compose restart          # Restart services"
    echo ""
    echo "  Dashboard: http://localhost:${DASHBOARD_PORT}"
    echo ""

    if $IS_WSL; then
        print_header "WSL2 Notes"
        echo ""
        echo "  - Docker Desktop must be running for the containers to work."
        echo "  - PostgreSQL runs inside WSL2 and must be started after each reboot:"
        echo "      sudo service postgresql start"
        echo "  - Ollama must be running (either Windows app or WSL2 service) before"
        echo "    starting the gateway."
        echo "  - To auto-start services on WSL2 boot, add to ~/.bashrc:"
        echo "      sudo service postgresql start 2>/dev/null"
        echo ""
    fi
}

main "$@"
