#!/bin/sh
# Stop — Uncommitted Changes Guard
# Fires when Claude tries to stop. Checks for uncommitted changes in any
# project that was edited during this session. Each project under
# /workspace/projects/ has its own independent git repo — there is NO
# repo at the workspace root.

MANIFEST="/tmp/claude-edit-manifest.md"
PROJECTS_DIR="/workspace/projects"

# If no manifest exists, nothing was edited — safe to stop
[ ! -f "$MANIFEST" ] || [ ! -s "$MANIFEST" ] && exit 0

# Extract unique project directories from the edit manifest.
# File paths look like: /workspace/projects/claude-gateway/internal/foo.go
# We extract the project name (third path component under /workspace/projects/).
PROJECTS=$(grep -oE '/workspace/projects/[^/]+/' "$MANIFEST" 2>/dev/null | sort -u)

[ -z "$PROJECTS" ] && exit 0

WARN_FILE=$(mktemp)

for PROJECT_PATH in $PROJECTS; do
    # Strip trailing slash for display
    PROJECT_NAME=$(basename "$PROJECT_PATH")

    # Skip if not a git repo
    [ ! -d "${PROJECT_PATH}.git" ] && continue

    # Get git status (porcelain for machine-readable output)
    STATUS=$(cd "$PROJECT_PATH" && git status --porcelain 2>/dev/null)
    [ -z "$STATUS" ] && continue

    # Count modified and untracked files
    MODIFIED=$(echo "$STATUS" | grep -c '^ M\|^M \|^MM\|^AM\|^ D\|^D \|^R ' 2>/dev/null || echo "0")
    UNTRACKED=$(echo "$STATUS" | grep -c '^??' 2>/dev/null || echo "0")
    STAGED=$(echo "$STATUS" | grep -c '^[MADRC] ' 2>/dev/null || echo "0")

    echo "" >> "$WARN_FILE"
    echo "[${PROJECT_NAME}] ${MODIFIED} modified, ${UNTRACKED} untracked, ${STAGED} staged:" >> "$WARN_FILE"
    # Show the actual files (truncate to first 15 to avoid massive output)
    echo "$STATUS" | head -15 >> "$WARN_FILE"
    TOTAL=$(echo "$STATUS" | wc -l | tr -d ' ')
    if [ "$TOTAL" -gt 15 ]; then
        echo "  ... and $((TOTAL - 15)) more files" >> "$WARN_FILE"
    fi
done

if [ -s "$WARN_FILE" ]; then
    WARNINGS=$(cat "$WARN_FILE")
    # Escape newlines and quotes for JSON
    WARNINGS_ESCAPED=$(printf '%s' "$WARNINGS" | sed 's/"/\\"/g' | tr '\n' '|' | sed 's/|/\\n/g')
    REASON="UNCOMMITTED CHANGES DETECTED before stop:\\n${WARNINGS_ESCAPED}\\n\\nYou MUST either: (1) commit these changes with a descriptive message, (2) explain to the user why changes are left uncommitted, or (3) stash them. Do NOT stop with uncommitted work."
    printf '{"decision":"block","reason":"%s"}\n' "$REASON"
fi

rm -f "$WARN_FILE"
exit 0
