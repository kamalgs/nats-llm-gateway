#!/bin/bash
set -e

MODEL="${MODEL:-qwen2.5:0.5b}"
OLLAMA_PORT=11434

echo "=== Edge sidecar starting ==="

# 1. Start Ollama on localhost (same container, no network overhead)
export OLLAMA_HOST="127.0.0.1:$OLLAMA_PORT"
ollama serve &
OLLAMA_PID=$!

# Wait for Ollama to be ready
echo "Waiting for Ollama..."
for i in $(seq 1 30); do
  if ollama list >/dev/null 2>&1; then
    echo "Ollama ready"
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "ERROR: Ollama not ready after 30s"
    exit 1
  fi
  sleep 1
done

# Pull model if not already present
echo "Ensuring model $MODEL is available..."
ollama pull "$MODEL"
echo "Model $MODEL ready"

# 2. Start NATS leaf node
echo "Starting NATS leaf node..."
nats-server --config /etc/nats/leaf.conf &
NATS_PID=$!
sleep 1

# 3. Start provider adapter (connects to local NATS leaf and local Ollama)
export PROVIDER_NAME=ollama
export PROVIDER_CONFIG=/etc/infermesh/demo.yaml
export NATS_URL="nats://127.0.0.1:4224"
export OLLAMA_BASE_URL="http://127.0.0.1:$OLLAMA_PORT"

echo "Starting provider adapter..."
provider &
PROVIDER_PID=$!

echo "=== Edge sidecar ready ==="

# Wait for any child to exit (then stop all)
wait -n $OLLAMA_PID $NATS_PID $PROVIDER_PID 2>/dev/null || true
echo "A process exited, shutting down..."
kill $OLLAMA_PID $NATS_PID $PROVIDER_PID 2>/dev/null || true
wait
