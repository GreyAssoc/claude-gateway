#!/bin/sh
# SessionStart hook — outputs environment + personality + active state to stdout.
# Claude Code injects stdout from exit-0 hooks into the conversation context.
# Fires on: startup, resume, compact, clear.

CONTEXT_DIR="/app/hooks/context"
PROJECTS_DIR="/workspace/projects"
REGISTRY_PATH="$CONTEXT_DIR/project-registry.json"

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

# 1. Active project — enriched with registry data (server, URL, deploy path)
ACTIVE_FILE="/tmp/claude-active-project"
if [ -f "$ACTIVE_FILE" ] && [ -s "$ACTIVE_FILE" ]; then
    ACTIVE_PROJECT=$(cat "$ACTIVE_FILE")
    echo "## Active Project"
    echo ""
    if [ -f "$REGISTRY_PATH" ] && command -v python3 >/dev/null 2>&1; then
        python3 -c "
import json, sys
try:
    reg = json.load(open('$REGISTRY_PATH'))
    proj = reg.get('projects', {}).get('$ACTIVE_PROJECT')
    if not proj:
        print('**Active project:** $ACTIVE_PROJECT')
        sys.exit(0)
    print('**Active project:** $ACTIVE_PROJECT')
    print('**Path:** ' + proj.get('path', ''))
    if proj.get('tech'): print('**Tech:** ' + proj['tech'])
    if proj.get('status'): print('**Status:** ' + proj['status'])
    for env, info in (proj.get('server') or {}).items():
        parts = [env + ': ' + (info.get('ip') or 'local')]
        if info.get('url'): parts.append(info['url'])
        if info.get('deploy_path'): parts.append('deploy: ' + info['deploy_path'])
        if info.get('ssh_key'): parts.append('key: ' + info['ssh_key'])
        print('**Server (' + env + '):** ' + ' | '.join(parts))
    if proj.get('git', {}).get('remote'): print('**Git:** ' + proj['git']['remote'])
    if proj.get('database', {}).get('type'): print('**Database:** ' + proj['database']['type'])
except Exception:
    print('**Active project:** $ACTIVE_PROJECT')
" 2>/dev/null
    else
        echo "**Active project:** ${ACTIVE_PROJECT}"
    fi
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
