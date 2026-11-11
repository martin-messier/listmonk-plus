#!/usr/bin/env bash
# listmonk-plus: (re)start Listmonk inside the dev container after code changes.
# Usage: plus/scripts/dev-restart.sh
# Requires the dev containers to be up: docker compose -f dev/docker-compose.yml up -d --wait
set -euo pipefail
cd "$(dirname "$0")/../.."

dc() { docker compose -f dev/docker-compose.yml "$@"; }

# Stop the running instance. pkill is exec'd directly (not via sh -c) so that it
# can't match its own wrapper shell's command line.
dc exec -T dev pkill -f -- '--config=dev/config.toml' || true
dc exec -T dev pkill -x make || true
for _ in $(seq 1 20); do
  curl -fsS -o /dev/null http://localhost:9000/health 2>/dev/null || break
  sleep 0.5
done

dc exec -d dev sh -c 'make run > /tmp/listmonk-run.log 2>&1'

for i in $(seq 1 120); do
  sleep 2
  if curl -fsS -o /dev/null http://localhost:9000/health 2>/dev/null; then
    echo "Listmonk is up (~$((i * 2))s). Log: docker compose -f dev/docker-compose.yml exec dev tail -f /tmp/listmonk-run.log"
    exit 0
  fi
  if ! dc exec -T dev pgrep -f -- '--config=dev/config.toml' >/dev/null && ! dc exec -T dev pgrep -x make >/dev/null; then
    echo "Listmonk exited during startup. Last log lines:" >&2
    dc exec -T dev tail -30 /tmp/listmonk-run.log >&2
    exit 1
  fi
done
echo "Timed out waiting for Listmonk to start." >&2
exit 1
