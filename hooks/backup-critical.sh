#!/bin/sh
# PreToolUse (Write) — Backup Critical Files
# Before overwriting key configuration or entry-point files, creates a
# timestamped backup in /tmp/claude-backups/. Keeps max 5 backups per
# file (rotates oldest). Silent — no output to Claude unless backup fails.

BACKUP_DIR="/tmp/claude-backups"
MAX_BACKUPS=5

# Critical files that get backed up before overwrite.
# Paths are relative patterns matched against the end of file_path.
CRITICAL_PATTERNS="
gateway.yaml
docker-compose.yml
.env
sidecar/index.js
cmd/gateway/main.go
Dockerfile.gateway
Dockerfile.sidecar
Makefile
claude-code-settings.json
"

# Read hook input
INPUT_FILE=$(mktemp)
cat > "$INPUT_FILE"

FILE_PATH=$(jq -r '.tool_input.file_path // empty' "$INPUT_FILE" 2>/dev/null)
rm -f "$INPUT_FILE"

[ -z "$FILE_PATH" ] && exit 0
[ ! -f "$FILE_PATH" ] && exit 0

# Check if this file matches any critical pattern
IS_CRITICAL=false
for PATTERN in $CRITICAL_PATTERNS; do
    [ -z "$PATTERN" ] && continue
    case "$FILE_PATH" in
        *"$PATTERN") IS_CRITICAL=true; break ;;
    esac
done

[ "$IS_CRITICAL" = "false" ] && exit 0

# Create backup directory
mkdir -p "$BACKUP_DIR" 2>/dev/null

# Generate backup filename: original_name.HHMMSS.bak
BASENAME=$(basename "$FILE_PATH")
TIMESTAMP=$(date '+%Y%m%d_%H%M%S')
BACKUP_PATH="${BACKUP_DIR}/${BASENAME}.${TIMESTAMP}.bak"

# Create backup
if cp "$FILE_PATH" "$BACKUP_PATH" 2>/dev/null; then
    # Rotate: keep only the latest MAX_BACKUPS copies of this file
    BACKUP_COUNT=$(ls -1t "${BACKUP_DIR}/${BASENAME}".*.bak 2>/dev/null | wc -l | tr -d ' ')
    if [ "$BACKUP_COUNT" -gt "$MAX_BACKUPS" ]; then
        ls -1t "${BACKUP_DIR}/${BASENAME}".*.bak 2>/dev/null | tail -n +"$((MAX_BACKUPS + 1))" | while read -r OLD; do
            rm -f "$OLD"
        done
    fi
    # Silent success — don't clutter Claude's context
    exit 0
else
    # Backup failed — warn Claude but don't block
    echo "WARNING: Failed to backup critical file ${FILE_PATH} before overwrite. Proceeding anyway."
    exit 0
fi
