#!/bin/bash
set -e

CLAUDE_DIR="/home/vscode/.claude"
JAGR_DIR="/opt/jagr"

sudo mkdir -p $JAGR_DIR
sudo chown -R vscode:vscode $JAGR_DIR



sudo chown -R vscode:vscode "$CLAUDE_DIR"

# echo '{}' > "$CLAUDE_DIR/claude.json"
# ln -sf "$CLAUDE_DIR/claude.json" /home/vscode/.claude.json

# cat > "$CLAUDE_DIR/settings.json" <<'EOF'
# {"permissions":{"defaultMode":"bypassPermissions"},"skipDangerousModePermissionPrompt":true}
# EOF

DEBIAN_FRONTEND=noninteractive sudo apt-get -y update
DEBIAN_FRONTEND=noninteractive sudo apt-get -y install direnv libncurses-dev zstd libncurses6 libncursesw6

# Add direnv hook to .bashrc if not already present
if ! grep -q 'direnv hook bash' ~/.bashrc; then
  echo 'eval "$(direnv hook bash)"' >> ~/.bashrc
fi

cd /workspaces/jagr
direnv allow