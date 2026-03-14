#!/bin/sh
# PreToolUse (Bash/Read/Edit/Write) — Project Isolation Enforcer
# Detects when Claude tries to access files in a DIFFERENT project than
# the one it's currently working in. Each project under /workspace/projects/
# is completely independent — no cross-project access allowed.
#
# How it works:
# 1. Reads the current "active project" from /tmp/claude-active-project
#    (set by the session-start hook or first file access)
# 2. Extracts the target project from the tool input
# 3. Blocks if they differ (with exceptions for workspace-root files)

ACTIVE_FILE="/tmp/claude-active-project"
PROJECTS_DIR="/workspace/projects"

# Read hook input
INPUT_FILE=$(mktemp)
cat > "$INPUT_FILE"

TOOL=$(jq -r '.tool_name // empty' "$INPUT_FILE" 2>/dev/null)

# Extract the target path based on tool type
TARGET_PATH=""
case "$TOOL" in
    Bash)
        # Extract file paths from the command that reference /workspace/projects/
        COMMAND=$(jq -r '.tool_input.command // empty' "$INPUT_FILE" 2>/dev/null)
        # Find all project references in the command
        TARGET_PROJECTS=$(printf '%s' "$COMMAND" | grep -oE '/workspace/projects/[^/ "'"'"']+' 2>/dev/null | sort -u)
        ;;
    Read)
        TARGET_PATH=$(jq -r '.tool_input.file_path // empty' "$INPUT_FILE" 2>/dev/null)
        ;;
    Edit|Write)
        TARGET_PATH=$(jq -r '.tool_input.file_path // empty' "$INPUT_FILE" 2>/dev/null)
        ;;
    *)
        rm -f "$INPUT_FILE"
        exit 0
        ;;
esac
rm -f "$INPUT_FILE"

# For Read/Edit/Write, extract project from path
if [ -n "$TARGET_PATH" ]; then
    TARGET_PROJECTS=$(printf '%s' "$TARGET_PATH" | grep -oE '/workspace/projects/[^/]+' 2>/dev/null)
fi

# No project references found — not our concern
[ -z "$TARGET_PROJECTS" ] && exit 0

# Load active project (if set)
ACTIVE_PROJECT=""
if [ -f "$ACTIVE_FILE" ]; then
    ACTIVE_PROJECT=$(cat "$ACTIVE_FILE" 2>/dev/null)
fi

# If no active project is set, set it from the first access and allow
if [ -z "$ACTIVE_PROJECT" ]; then
    FIRST_PROJECT=$(echo "$TARGET_PROJECTS" | head -1)
    FIRST_NAME=$(basename "$FIRST_PROJECT")
    echo "$FIRST_NAME" > "$ACTIVE_FILE"
    exit 0
fi

# Check each referenced project against the active one
VIOLATIONS=""
for PROJECT_REF in $TARGET_PROJECTS; do
    PROJECT_NAME=$(basename "$PROJECT_REF")
    if [ "$PROJECT_NAME" != "$ACTIVE_PROJECT" ]; then
        VIOLATIONS="${VIOLATIONS} ${PROJECT_NAME}"
    fi
done

# No violations
[ -z "$VIOLATIONS" ] && exit 0

# Trim leading space
VIOLATIONS=$(echo "$VIOLATIONS" | sed 's/^ //')

REASON="PROJECT ISOLATION VIOLATION: You are currently working on '${ACTIVE_PROJECT}' but this operation accesses project(s): ${VIOLATIONS}. Each project under /workspace/projects/ is COMPLETELY INDEPENDENT — no cross-project file access allowed. If you need to switch projects, tell the user explicitly and they will confirm."
REASON=$(printf '%s' "$REASON" | sed 's/"/\\"/g')
printf '{"decision":"block","reason":"%s"}\n' "$REASON"

exit 0
