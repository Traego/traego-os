#!/bin/sh
# Traego installer — served from traego.ai/install.sh (get.traego.io later).
#
#   Run a controller (the first machine, leads the mesh):
#     curl -fsSL https://traego.ai/install.sh | sh -s -- controller
#
#   Join a machine to an existing controller:
#     curl -fsSL https://traego.ai/install.sh | sh -s -- join --controller http://192.168.1.10:8443 --token <JOIN_TOKEN>
#
# POSIX sh, no bashisms: this runs on whatever box a homelab has.
set -eu

IMAGE_CONTROLLER="${TRAEGO_IMAGE_CONTROLLER:-ghcr.io/traego/controller:latest}"
IMAGE_TRAEGOD="${TRAEGO_IMAGE_TRAEGOD:-ghcr.io/traego/traegod:latest}"

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "docker is required — install it first (https://docs.docker.com/engine/install/)"
docker info >/dev/null 2>&1 || die "docker is installed but the daemon isn't reachable (is it running? do you need sudo?)"

rand_hex() {
  if command -v openssl >/dev/null 2>&1; then openssl rand -hex 16; else
    head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n'; fi
}

MODE="${1:-}"
[ -n "$MODE" ] && shift || true

case "$MODE" in
  controller)
    ADMIN_KEY=$(rand_hex)
    JOIN_TOKEN=$(rand_hex)
    say "Starting the Traego controller…"
    docker run -d --name traego-controller --restart unless-stopped \
      -p 8443:8443 -p 8444:8444 \
      -e TRAEGO_ADMIN_KEY="$ADMIN_KEY" \
      -e TRAEGO_JOIN_TOKEN="$JOIN_TOKEN" \
      -e TRAEGO_DB=/data/traego.db \
      -v traego-data:/data \
      "$IMAGE_CONTROLLER" >/dev/null
    HOST_IP=$(hostname -I 2>/dev/null | awk '{print $1}') || HOST_IP=""
    [ -n "$HOST_IP" ] || HOST_IP="<this-machine>"
    say ""
    say "  Dashboard:   http://${HOST_IP}:8443"
    say "  Admin key:   $ADMIN_KEY   (enter it in the dashboard topbar)"
    say "  Join token:  $JOIN_TOKEN  (machines present this to join)"
    say ""
    say "Join another machine with:"
    say "  curl -fsSL https://traego.ai/install.sh | sh -s -- join --controller http://${HOST_IP}:8443 --token $JOIN_TOKEN"
    ;;

  join)
    CONTROLLER="" TOKEN="" NAME="$(hostname 2>/dev/null || echo traego-node)"
    while [ $# -gt 0 ]; do
      case "$1" in
        --controller) CONTROLLER="$2"; shift 2 ;;
        --token)      TOKEN="$2"; shift 2 ;;
        --name)       NAME="$2"; shift 2 ;;
        *) die "unknown flag: $1" ;;
      esac
    done
    [ -n "$CONTROLLER" ] || die "--controller is required (e.g. --controller http://192.168.1.10:8443)"
    [ -n "$TOKEN" ] || die "--token is required (shown when the controller was installed)"
    say "Joining $CONTROLLER as '$NAME'…"
    docker run -d --name traegod --restart unless-stopped \
      -e TRAEGO_CONTROLLER="$CONTROLLER" \
      -e TRAEGO_JOIN_TOKEN="$TOKEN" \
      -e TRAEGO_NODE_NAME="$NAME" \
      -v traegod-data:/data \
      "$IMAGE_TRAEGOD" >/dev/null
    say "Waiting for the pairing code…"
    i=0
    while [ $i -lt 30 ]; do
      CODE=$(docker logs traegod 2>&1 | sed -n 's/.*pairing code: \([A-Z0-9-]*\).*/\1/p' | head -1)
      if [ -n "$CODE" ]; then
        say ""
        say "  Pairing code: $CODE"
        say ""
        say "Open your Traego dashboard → Hardware → 'Waiting for adoption' and enter it."
        exit 0
      fi
      i=$((i+1)); sleep 1
    done
    say "No pairing code yet — check: docker logs -f traegod"
    ;;

  *)
    say "Traego installer"
    say ""
    say "  controller                      run the controller on this machine"
    say "  join --controller URL --token T join this machine to a controller"
    exit 1
    ;;
esac
