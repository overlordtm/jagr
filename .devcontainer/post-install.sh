#!/bin/bash
set -e

CLAUDE_DIR="/home/vscode/.claude"

sudo chown -R vscode:vscode "$CLAUDE_DIR"

echo '{}' > "$CLAUDE_DIR/claude.json"
ln -sf "$CLAUDE_DIR/claude.json" /home/vscode/.claude.json

cat > "$CLAUDE_DIR/settings.json" <<'EOF'
{"permissions":{"defaultMode":"bypassPermissions"},"skipDangerousModePermissionPrompt":true}
EOF

DEBIAN_FRONTEND=noninteractive apt-get -y update
DEBIAN_FRONTEND=noninteractive apt-get -y install libncurses-dev libncurses6 libncursesw6
