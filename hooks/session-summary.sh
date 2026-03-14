#!/bin/sh
# Stop — Session Summary to Memory
# When Claude's session ends, generates a compact summary of what was done
# and writes it to the daily memory directory. This feeds into the memory
# compactor's nightly job, ensuring nothing is lost between sessions.

MANIFEST="/tmp/claude-edit-manifest.md"
MEMORY_DIR="/workspace/projects/claude-gateway/memory/daily"
SUMMARY_FILE="${MEMORY_DIR}/session-$(date '+%Y-%m-%d_%H%M%S').md"

# Nothing to summarise if no edits were made
[ ! -f "$MANIFEST" ] || [ ! -s "$MANIFEST" ] && exit 0

# Ensure memory directory exists
mkdir -p "$MEMORY_DIR" 2>/dev/null

# Count edits from manifest
EDIT_COUNT=$(grep -c '^\- \[' "$MANIFEST" 2>/dev/null || echo "0")

# Extract unique files edited
FILES_EDITED=$(grep -oE '`[^`]+`' "$MANIFEST" 2>/dev/null | sort -u | tr '\n' ', ' | sed 's/, $//')

# Extract unique projects touched
PROJECTS_TOUCHED=$(grep -oE '/workspace/projects/[^/]+/' "$MANIFEST" 2>/dev/null | sort -u | while read -r p; do basename "$p"; done | tr '\n' ', ' | sed 's/, $//')

# Check for uncommitted changes across touched projects
GIT_STATUS=""
for PROJECT_PATH in $(grep -oE '/workspace/projects/[^/]+/' "$MANIFEST" 2>/dev/null | sort -u); do
    [ ! -d "${PROJECT_PATH}.git" ] && continue
    PROJECT_NAME=$(basename "$PROJECT_PATH")
    CHANGES=$(cd "$PROJECT_PATH" && git status --porcelain 2>/dev/null | wc -l | tr -d ' ')
    if [ "$CHANGES" -gt 0 ]; then
        LAST_COMMIT=$(cd "$PROJECT_PATH" && git log --oneline -1 2>/dev/null || echo "no commits")
        GIT_STATUS="${GIT_STATUS}\n- ${PROJECT_NAME}: ${CHANGES} uncommitted files (last commit: ${LAST_COMMIT})"
    else
        LAST_COMMIT=$(cd "$PROJECT_PATH" && git log --oneline -1 2>/dev/null || echo "no commits")
        GIT_STATUS="${GIT_STATUS}\n- ${PROJECT_NAME}: clean (last commit: ${LAST_COMMIT})"
    fi
done

# Write the summary
cat > "$SUMMARY_FILE" << EOF
# Session Summary — $(date '+%Y-%m-%d %H:%M')

## Edits
- **Total edits:** ${EDIT_COUNT}
- **Files:** ${FILES_EDITED}
- **Projects:** ${PROJECTS_TOUCHED:-none detected}

## Git Status
$(printf '%b' "$GIT_STATUS")

## Edit Log
$(cat "$MANIFEST")
EOF

# Clean up the manifest (session is ending)
rm -f "$MANIFEST"

# Silent — no output to Claude (this runs on stop, output would be ignored anyway)
exit 0
