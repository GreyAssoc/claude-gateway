#!/bin/sh
# PostToolUse (Edit/Write) — Compaction Shield
# Tracks every file edit/write during the session in a manifest file.
# The SessionStart hook reads this manifest back after compaction so
# Claude doesn't lose track of what it changed mid-implementation.

MANIFEST="/tmp/claude-edit-manifest.md"

# Read hook input into temp file
INPUT_FILE=$(mktemp)
cat > "$INPUT_FILE"

TOOL=$(jq -r '.tool_name // empty' "$INPUT_FILE" 2>/dev/null)
FILE_PATH=$(jq -r '.tool_input.file_path // empty' "$INPUT_FILE" 2>/dev/null)

[ -z "$FILE_PATH" ] && rm -f "$INPUT_FILE" && exit 0

TIMESTAMP=$(date '+%H:%M:%S')

# Determine action and detail
if [ "$TOOL" = "Write" ]; then
    ACTION="created/overwrote"
    LINES=$(jq -r '.tool_input.content // empty' "$INPUT_FILE" 2>/dev/null | wc -l | tr -d ' ')
    DETAIL="${LINES} lines"
elif [ "$TOOL" = "Edit" ]; then
    ACTION="edited"
    OLD_LINES=$(jq -r '.tool_input.old_string // empty' "$INPUT_FILE" 2>/dev/null | wc -l | tr -d ' ')
    NEW_LINES=$(jq -r '.tool_input.new_string // empty' "$INPUT_FILE" 2>/dev/null | wc -l | tr -d ' ')
    DETAIL="${OLD_LINES}->${NEW_LINES} lines"
else
    rm -f "$INPUT_FILE"
    exit 0
fi
rm -f "$INPUT_FILE"

# Create manifest header if it doesn't exist
if [ ! -f "$MANIFEST" ]; then
    cat > "$MANIFEST" << 'HEADER'
# Session Edit Manifest
Files modified during this session (survives compaction):

HEADER
fi

# Append entry
echo "- [${TIMESTAMP}] ${ACTION} \`${FILE_PATH}\` (${DETAIL})" >> "$MANIFEST"

# Silent — no output to Claude (this hook just records)
exit 0
