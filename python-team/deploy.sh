#!/bin/bash
# Deploy Python team agents to production

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_DIR="/workspace/.claude/agents/coding/python-team"

echo "=== Python Team Deployment ==="
echo ""

# Step 1: Create python-team directory if it doesn't exist
echo "1. Creating python-team directory..."
mkdir -p "$AGENT_DIR"
echo "   ✓ Directory created: $AGENT_DIR"
echo ""

# Step 2: Copy Python agent DNA files
echo "2. Copying Python agent DNA files..."
cp -v "$SCRIPT_DIR/python-purist.md" "$AGENT_DIR/"
cp -v "$SCRIPT_DIR/python-pragmatist.md" "$AGENT_DIR/"
cp -v "$SCRIPT_DIR/python-pessimist.md" "$AGENT_DIR/"
cp -v "$SCRIPT_DIR/python-security-agent.md" "$AGENT_DIR/"
cp -v "$SCRIPT_DIR/python-hacker-agent.md" "$AGENT_DIR/"
echo "   ✓ All Python agent DNA files copied"
echo ""

# Step 3: Update coding-agent with Python section
echo "3. Updating coding-agent.md with Python section..."
CODING_AGENT="/workspace/.claude/agents/coding/coding-agent.md"

# Check if Python section already exists
if grep -q "## Python-Specific Fix Patterns" "$CODING_AGENT"; then
    echo "   ⚠ Python section already exists in coding-agent.md, skipping..."
else
    # Update language list
    sed -i 's/\*\*Languages:\*\* Go, JavaScript (vanilla ES2020+), CSS3, Kotlin/**Languages:** Go, JavaScript (vanilla ES2020+), CSS3, Kotlin, Python/' "$CODING_AGENT"

    # Append Python section
    echo "" >> "$CODING_AGENT"
    cat "$SCRIPT_DIR/python-coding-agent-section.md" >> "$CODING_AGENT"

    echo "   ✓ Python section added to coding-agent.md"
fi
echo ""

# Step 4: Run tests
echo "4. Running orchestrator tests..."
cd /workspace/projects/claude-gateway
go test ./internal/orchestrator/... -v
echo "   ✓ Tests passed"
echo ""

# Step 5: Build the gateway
echo "5. Building gateway..."
go build -o bin/gateway ./cmd/gateway
echo "   ✓ Gateway built successfully"
echo ""

# Step 6: Show deployment summary
echo "=== Deployment Summary ==="
echo ""
echo "Python agent files installed:"
ls -lh "$AGENT_DIR"
echo ""
echo "Available teams:"
echo "  - python-dev-team (PythonPurist, PythonPragmatist, PythonPessimist)"
echo "  - python-sec-team (PythonSecurity, PythonHacker)"
echo ""
echo "Bot commands:"
echo "  /review_python <task>     - Run Python development team review"
echo "  /review_python_sec <task> - Run Python security team review"
echo ""
echo "Language detection:"
echo "  .py files → python-dev-team, python-sec-team"
echo ""
echo "✅ Deployment complete!"
echo ""
echo "Next steps:"
echo "  1. Restart the gateway service:"
echo "     docker-compose restart gateway"
echo ""
echo "  2. Test with the Google project:"
echo "     /review_python Review /workspace/projects/google/app.py"
echo ""
echo "  3. Run security review:"
echo "     /review_python_sec Review /workspace/projects/google/app.py for security issues"
echo ""
