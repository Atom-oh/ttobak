#!/usr/bin/env bash
# Notification hook — receives notifications about task completion
# Extend this to integrate with Slack, email, or other notification systems
set -euo pipefail

# Read notification data from stdin
INPUT=$(cat 2>/dev/null || true)

# Log notifications (extend with webhook/Slack integration as needed)
echo "[$(date -Iseconds)] Notification received" >> /tmp/ttobak-claude-notifications.log 2>/dev/null || true

exit 0
