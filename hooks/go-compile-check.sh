#!/bin/sh
# PostToolUse (Edit/Write) — Go Compilation Check
# After editing .go files, runs `go vet` on the package to catch errors
# immediately. Outputs violations so Claude fixes them before moving on.

# Read hook input
INPUT_FILE=$(mktemp)
cat > "$INPUT_FILE"

FILE_PATH=$(jq -r '.tool_input.file_path // empty' "$INPUT_FILE" 2>/dev/null)
rm -f "$INPUT_FILE"

[ -z "$FILE_PATH" ] && exit 0

# Only check .go files
case "$FILE_PATH" in
    *.go) ;;
    *) exit 0 ;;
esac

# Find the module root (directory containing go.mod)
DIR=$(dirname "$FILE_PATH")
MODULE_ROOT=""
CHECK_DIR="$DIR"
while [ "$CHECK_DIR" != "/" ] && [ "$CHECK_DIR" != "." ]; do
    if [ -f "${CHECK_DIR}/go.mod" ]; then
        MODULE_ROOT="$CHECK_DIR"
        break
    fi
    CHECK_DIR=$(dirname "$CHECK_DIR")
done

[ -z "$MODULE_ROOT" ] && exit 0

# Get the package path relative to module root for targeted check
REL_PKG=$(echo "$DIR" | sed "s|^${MODULE_ROOT}||" | sed 's|^/||')
if [ -z "$REL_PKG" ]; then
    PKG_PATH="."
else
    PKG_PATH="./${REL_PKG}"
fi

# Run go vet on just the affected package (fast, ~1-2s)
VET_OUTPUT=$(cd "$MODULE_ROOT" && go vet "$PKG_PATH" 2>&1)
VET_EXIT=$?

if [ $VET_EXIT -ne 0 ] && [ -n "$VET_OUTPUT" ]; then
    # Truncate to 500 chars to avoid flooding context
    VET_SHORT=$(printf '%s' "$VET_OUTPUT" | head -20 | head -c 500)
    VET_SHORT=$(printf '%s' "$VET_SHORT" | sed 's/"/\\"/g' | tr '\n' '|' | sed 's/|/\\n/g')
    REASON="GO VET FAILED after editing ${FILE_PATH}:\\n${VET_SHORT}\\n\\nFix these issues before continuing."
    printf '{"decision":"block","reason":"%s"}\n' "$REASON"
fi

exit 0
