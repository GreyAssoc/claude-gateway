#!/bin/sh
# PostToolUse (Bash) — Retry Detector
# Tracks failed bash commands. After 3 consecutive failures of the same
# command pattern, outputs a JSON decision that tells Claude to stop
# retrying and try a different approach.

HISTORY_FILE="/tmp/claude-bash-failures.jsonl"
MAX_RETRIES=3

# Read hook input from stdin into a temp file (handles large inputs)
INPUT_FILE=$(mktemp)
cat > "$INPUT_FILE"

COMMAND=$(jq -r '.tool_input.command // empty' "$INPUT_FILE" 2>/dev/null)
EXIT_CODE=$(jq -r '.tool_response.exitCode // 0' "$INPUT_FILE" 2>/dev/null)
SUCCESS=$(jq -r '.tool_response.success // true' "$INPUT_FILE" 2>/dev/null)
rm -f "$INPUT_FILE"

# If we can't parse the command, exit silently
[ -z "$COMMAND" ] && exit 0

# Determine if the command failed
FAILED=false
if [ "$SUCCESS" = "false" ] || [ "$EXIT_CODE" != "0" ]; then
    FAILED=true
fi

# Normalise command to a hash for comparison
CMD_HASH=$(printf '%s' "$COMMAND" | md5sum | cut -d' ' -f1)

if [ "$FAILED" = "false" ]; then
    # Success — clear failure history for this command pattern
    if [ -f "$HISTORY_FILE" ]; then
        grep -v "\"$CMD_HASH\"" "$HISTORY_FILE" > "${HISTORY_FILE}.tmp" 2>/dev/null
        mv "${HISTORY_FILE}.tmp" "$HISTORY_FILE" 2>/dev/null
    fi
    exit 0
fi

# Failed — record it
CMD_SHORT=$(printf '%s' "$COMMAND" | head -c 200 | tr '"' "'")
echo "{\"hash\":\"$CMD_HASH\",\"cmd\":\"$CMD_SHORT\"}" >> "$HISTORY_FILE"

# Count consecutive failures for this command pattern
COUNT=$(grep -c "\"$CMD_HASH\"" "$HISTORY_FILE" 2>/dev/null || echo "0")

if [ "$COUNT" -ge "$MAX_RETRIES" ]; then
    # Clear history for this pattern after warning
    grep -v "\"$CMD_HASH\"" "$HISTORY_FILE" > "${HISTORY_FILE}.tmp" 2>/dev/null
    mv "${HISTORY_FILE}.tmp" "$HISTORY_FILE" 2>/dev/null

    # Output JSON decision so Claude sees the warning
    REASON="RETRY LIMIT: This command has failed ${COUNT} times consecutively. STOP retrying this exact approach. Diagnose the root cause from the error output, try a fundamentally different approach, or explain the failure to the user and ask for guidance."
    printf '{"decision":"block","reason":"%s"}\n' "$REASON"
fi

exit 0
