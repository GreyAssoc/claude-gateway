#!/bin/sh
# PostToolUse (Edit/Write) — Anti-Pattern Checker
# Runs regex patterns from patterns.json against changed file content.
# Outputs a JSON decision when violations are found so Claude fixes them.

PATTERNS_FILE="/app/hooks/patterns.json"

# Read hook input into temp file (handles large Write content)
INPUT_FILE=$(mktemp)
cat > "$INPUT_FILE"

TOOL=$(jq -r '.tool_name // empty' "$INPUT_FILE" 2>/dev/null)
FILE_PATH=$(jq -r '.tool_input.file_path // empty' "$INPUT_FILE" 2>/dev/null)

[ -z "$FILE_PATH" ] && rm -f "$INPUT_FILE" && exit 0
[ ! -f "$PATTERNS_FILE" ] && rm -f "$INPUT_FILE" && exit 0

# Extract changed content to a temp file
CONTENT_FILE=$(mktemp)
if [ "$TOOL" = "Edit" ]; then
    jq -r '.tool_input.new_string // empty' "$INPUT_FILE" > "$CONTENT_FILE" 2>/dev/null
elif [ "$TOOL" = "Write" ]; then
    jq -r '.tool_input.content // empty' "$INPUT_FILE" > "$CONTENT_FILE" 2>/dev/null
else
    rm -f "$INPUT_FILE" "$CONTENT_FILE"
    exit 0
fi
rm -f "$INPUT_FILE"

# Skip if no content to check
if [ ! -s "$CONTENT_FILE" ]; then
    rm -f "$CONTENT_FILE"
    exit 0
fi

# Determine applicable pattern groups from file extension
EXT="${FILE_PATH##*.}"
GROUPS=""
case "$EXT" in
    go)        GROUPS="go general" ;;
    js)        GROUPS="frontend general" ;;
    html|htm)  GROUPS="frontend general" ;;
    css)       GROUPS="frontend general" ;;
    mq[l45h])  GROUPS="general" ;;
    py)        GROUPS="general" ;;
    sh|bash)   GROUPS="general" ;;
    *)         GROUPS="general" ;;
esac

# Check patterns and collect warnings
WARN_FILE=$(mktemp)
for GROUP in $GROUPS; do
    # Extract patterns as tab-separated: pattern\tmessage\tseverity
    jq -r ".${GROUP}[]? | [.pattern, .message, .severity] | @tsv" "$PATTERNS_FILE" 2>/dev/null | \
    while IFS="$(printf '\t')" read -r REGEX MSG SEVERITY; do
        [ -z "$REGEX" ] && continue
        if grep -qE "$REGEX" "$CONTENT_FILE" 2>/dev/null; then
            echo "[${SEVERITY:-warning}] ${MSG}" >> "$WARN_FILE"
        fi
    done
done

if [ -s "$WARN_FILE" ]; then
    # Build reason string from all warnings
    WARNINGS=$(cat "$WARN_FILE" | tr '\n' '; ' | sed 's/; $//')
    REASON="ANTI-PATTERN in ${FILE_PATH}: ${WARNINGS}. Fix these violations before continuing."
    # Escape for JSON
    REASON=$(printf '%s' "$REASON" | sed 's/"/\\"/g')
    printf '{"decision":"block","reason":"%s"}\n' "$REASON"
fi

rm -f "$CONTENT_FILE" "$WARN_FILE"
exit 0
