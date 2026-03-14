#!/bin/sh
# SessionStart hook — outputs environment + personality + active state to stdout.
# Claude Code injects stdout from exit-0 hooks into the conversation context.
# Fires on: startup, resume, compact, clear.

CONTEXT_DIR="/app/hooks/context"
PROJECTS_DIR="/workspace/projects"

# --- Static Context ---
for f in "$CONTEXT_DIR"/environment.md "$CONTEXT_DIR"/personality.md; do
    if [ -f "$f" ]; then
        cat "$f"
        echo ""
    fi
done

# --- Compaction Shield: re-inject the edit manifest ---
MANIFEST="/tmp/claude-edit-manifest.md"
if [ -f "$MANIFEST" ] && [ -s "$MANIFEST" ]; then
    echo ""
    cat "$MANIFEST"
    echo ""
fi

# --- Active Task State Injection ---
# Inject dynamic state so Claude has full situational awareness on every
# session start, resume, and compaction recovery.

echo ""
echo "# Active State (auto-injected)"
echo ""

# 1. Active project (if set by isolation enforcer)
ACTIVE_FILE="/tmp/claude-active-project"
if [ -f "$ACTIVE_FILE" ] && [ -s "$ACTIVE_FILE" ]; then
    ACTIVE_PROJECT=$(cat "$ACTIVE_FILE")
    echo "**Active project:** ${ACTIVE_PROJECT}"
    echo ""
fi

# 2. Uncommitted changes across projects with recent edits
EDITED_PROJECTS=""
if [ -f "$MANIFEST" ]; then
    EDITED_PROJECTS=$(grep -oE '/workspace/projects/[^/]+/' "$MANIFEST" 2>/dev/null | sort -u)
fi

if [ -n "$EDITED_PROJECTS" ]; then
    HAS_CHANGES=false
    for PROJECT_PATH in $EDITED_PROJECTS; do
        [ ! -d "${PROJECT_PATH}.git" ] && continue
        PROJECT_NAME=$(basename "$PROJECT_PATH")
        STATUS=$(cd "$PROJECT_PATH" && git status --porcelain 2>/dev/null)
        if [ -n "$STATUS" ]; then
            if [ "$HAS_CHANGES" = "false" ]; then
                echo "## Uncommitted Changes"
                HAS_CHANGES=true
            fi
            COUNT=$(echo "$STATUS" | wc -l | tr -d ' ')
            echo "- **${PROJECT_NAME}:** ${COUNT} files changed"
            echo "$STATUS" | head -5 | sed 's/^/  /'
            if [ "$COUNT" -gt 5 ]; then
                echo "  ... and $((COUNT - 5)) more"
            fi
        fi
    done
    if [ "$HAS_CHANGES" = "true" ]; then
        echo ""
    fi
fi

# 3. Last session summary (most recent daily memory file)
MEMORY_DIR="/workspace/projects/claude-gateway/memory/daily"
if [ -d "$MEMORY_DIR" ]; then
    LATEST_SUMMARY=$(ls -1t "$MEMORY_DIR"/session-*.md 2>/dev/null | head -1)
    if [ -n "$LATEST_SUMMARY" ] && [ -f "$LATEST_SUMMARY" ]; then
        SUMMARY_AGE_HOURS=$(( ( $(date +%s) - $(stat -c %Y "$LATEST_SUMMARY" 2>/dev/null || echo "0") ) / 3600 ))
        if [ "$SUMMARY_AGE_HOURS" -lt 24 ]; then
            echo "## Previous Session ($(basename "$LATEST_SUMMARY" | sed 's/session-//' | sed 's/\.md//'))"
            # Inject just the summary header, not the full edit log
            sed -n '/^## Edits/,/^## Edit Log/p' "$LATEST_SUMMARY" 2>/dev/null | head -10
            echo ""
        fi
    fi
fi

# 4. Critical file backup status
BACKUP_DIR="/tmp/claude-backups"
if [ -d "$BACKUP_DIR" ]; then
    BACKUP_COUNT=$(ls -1 "$BACKUP_DIR"/*.bak 2>/dev/null | wc -l | tr -d ' ')
    if [ "$BACKUP_COUNT" -gt 0 ]; then
        echo "**Active backups:** ${BACKUP_COUNT} critical file backups in ${BACKUP_DIR}"
        echo ""
    fi
fi

exit 0
