#!/usr/bin/env bash
# listmonk-plus staging: build and (re)start the stack from the current checkout.
#
#   plus/deploy/staging/deploy.sh            pull the branch, rebuild, restart
#   plus/deploy/staging/deploy.sh --no-pull  rebuild what's checked out
#   plus/deploy/staging/deploy.sh --wait-only
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
ENV=/srv/listmonk-plus/.env
dc() { docker compose -f "$HERE/docker-compose.yml" --env-file "$ENV" "$@"; }

wait_healthy() {
  for i in $(seq 1 90); do
    if dc exec -T app wget -qO- http://localhost:9000/health >/dev/null 2>&1; then
      echo "Listmonk is up."
      return 0
    fi
    sleep 2
  done
  echo "Listmonk did not become healthy. Recent logs:" >&2
  dc logs --tail 40 app >&2
  return 1
}

if [ "${1:-}" = "--wait-only" ]; then
  wait_healthy
  exit
fi

if [ "${1:-}" != "--no-pull" ]; then
  git -C "$REPO" pull --ff-only
fi

export BUILD_COMMIT="$(git -C "$REPO" rev-parse --short HEAD)"
echo "Building listmonk-plus at $BUILD_COMMIT..."
dc build --pull app
dc up -d --remove-orphans
wait_healthy
docker image prune -f >/dev/null
