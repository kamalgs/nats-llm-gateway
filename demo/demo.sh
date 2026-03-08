#!/usr/bin/env bash
set -euo pipefail

PROXY_URL="${PROXY_URL:-http://localhost:8080}"
MODELS=("gpt-4o" "gpt-4o-mini" "claude-sonnet" "claude-haiku" "llama3" "mistral")

echo "=== InferMesh Multi-Provider Demo ==="
echo "Proxy: $PROXY_URL"
echo ""

# Wait for proxy to be ready
echo "Waiting for proxy health check..."
for i in $(seq 1 30); do
  if curl -sf "$PROXY_URL/health" > /dev/null 2>&1; then
    echo "Proxy is ready!"
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "ERROR: Proxy not ready after 30 seconds"
    exit 1
  fi
  sleep 1
done

echo ""
echo "--- Sending chat completions to all 6 models ---"
echo ""

for model in "${MODELS[@]}"; do
  echo ">>> Model: $model"
  response=$(curl -sf "$PROXY_URL/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{
      \"model\": \"$model\",
      \"messages\": [{\"role\": \"user\", \"content\": \"Say hello and identify yourself.\"}]
    }")

  # Extract and display key fields
  resp_model=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin)['model'])" 2>/dev/null || echo "N/A")
  content=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin)['choices'][0]['message']['content'])" 2>/dev/null || echo "N/A")
  tokens=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('usage',{}).get('total_tokens','N/A'))" 2>/dev/null || echo "N/A")

  echo "    Upstream model: $resp_model"
  echo "    Response: $content"
  echo "    Tokens: $tokens"
  echo ""
done

echo "=== Demo complete! All providers responded via unified API. ==="
