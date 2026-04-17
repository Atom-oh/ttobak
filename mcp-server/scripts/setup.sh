#!/bin/bash
set -euo pipefail

# Ttobak MCP Server Setup
# Discovers Cognito + CloudFront config from CloudFormation outputs
# and writes them into .mcp.json

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MCP_DIR="$SCRIPT_DIR/.."
REGION="${AWS_DEFAULT_REGION:-ap-northeast-2}"

echo "=== Ttobak MCP Server Setup ==="
echo ""

# 1. Install dependencies
echo "[1/3] Installing MCP server dependencies..."
cd "$MCP_DIR"
npm install --silent
npm run build
echo "  Done."

# 2. Discover config from CloudFormation
echo "[2/3] Querying CloudFormation outputs..."

SPA_CLIENT_ID=$(aws cloudformation describe-stacks \
  --stack-name TtobakAuthStack \
  --region "$REGION" \
  --query "Stacks[0].Outputs[?ExportName=='TtobakSpaClientId'].OutputValue" \
  --output text 2>/dev/null || echo "")

DOMAIN_URL=$(aws cloudformation describe-stacks \
  --stack-name TtobakAuthStack \
  --region "$REGION" \
  --query "Stacks[0].Outputs[?ExportName=='TtobakUserPoolDomainUrl'].OutputValue" \
  --output text 2>/dev/null || echo "")

CF_DOMAIN=$(aws cloudformation describe-stacks \
  --stack-name TtobakFrontendStack \
  --region "$REGION" \
  --query "Stacks[0].Outputs[?ExportName=='TtobakDistributionDomainName'].OutputValue" \
  --output text 2>/dev/null || echo "")

if [ -z "$SPA_CLIENT_ID" ] || [ -z "$DOMAIN_URL" ]; then
  echo "  ERROR: Could not read CloudFormation outputs."
  echo "  Make sure TtobakAuthStack is deployed and you have AWS credentials."
  echo ""
  echo "  You can set values manually in .mcp.json:"
  echo "    TTOBAK_COGNITO_DOMAIN  = <Cognito hosted UI URL>"
  echo "    TTOBAK_CLIENT_ID       = <SPA Client ID>"
  echo "    TTOBAK_API_URL         = <CloudFront URL>"
  exit 1
fi

API_URL="https://${CF_DOMAIN}"

echo "  SPA Client ID:   ${SPA_CLIENT_ID:0:8}..."
echo "  Cognito Domain:  $DOMAIN_URL"
echo "  API URL:         $API_URL"

# 3. Write .mcp.json
echo "[3/3] Writing .mcp.json..."

cat > "$PROJECT_ROOT/.mcp.json" <<EOF
{
  "mcpServers": {
    "ttobak": {
      "command": "node",
      "args": ["mcp-server/dist/index.js"],
      "env": {
        "TTOBAK_COGNITO_DOMAIN": "$DOMAIN_URL",
        "TTOBAK_CLIENT_ID": "$SPA_CLIENT_ID",
        "TTOBAK_API_URL": "$API_URL",
        "TTOBAK_REGION": "$REGION"
      }
    }
  }
}
EOF

echo "  Written to $PROJECT_ROOT/.mcp.json"
echo ""
echo "=== Setup Complete ==="
echo ""
echo "Next steps:"
echo "  1. Deploy CDK if you haven't (to register OAuth callback URL):"
echo "     cd infra && npx cdk deploy TtobakAuthStack"
echo "  2. Restart Claude Code to pick up the new MCP server"
echo "  3. Use ttobak_login to authenticate"
