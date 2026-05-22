#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

ACTION="${1:-install}"
IMAGE="${KANLY_IMAGE:-kanly-admin:local}"
CONTAINER="${KANLY_CONTAINER:-kanly-admin}"
HOST_PORT="${KANLY_PORT:-60000}"
CONTAINER_PORT=60000
DUNE_ROOT="${KANLY_DUNE_ROOT:-/srv/kanly/server/dune-awakening-selfhost-docker}"
DATA_DIR="${KANLY_DATA_DIR:-$PWD/.kanly-data}"

require_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        echo "docker is required but not installed." >&2
        exit 1
    fi
}

build_image() {
    echo "Building image: $IMAGE"
    docker build -t "$IMAGE" .
}

remove_container_if_exists() {
    if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER"; then
        docker rm -f "$CONTAINER" >/dev/null
    fi
}

run_container() {
    mkdir -p "$DATA_DIR"

    if [ ! -d "$DUNE_ROOT" ] || [ ! -f "$DUNE_ROOT/docker-compose.yml" ]; then
        echo "Dune root not found or invalid: $DUNE_ROOT" >&2
        echo "Set KANLY_DUNE_ROOT to your dune-awakening-selfhost-docker path." >&2
        exit 1
    fi

    remove_container_if_exists

    echo "Starting container: $CONTAINER"
    docker run -d \
        --name "$CONTAINER" \
        --restart unless-stopped \
        -p "$HOST_PORT:$CONTAINER_PORT" \
        -e PORT="$CONTAINER_PORT" \
        -e KANLY_DUNE_ROOT=/dune \
        -e KANLY_DB_PATH=/app/data/kanly.db \
        -v "$DATA_DIR:/app/data" \
        -v "$DUNE_ROOT:/dune" \
        -v /var/run/docker.sock:/var/run/docker.sock \
        "$IMAGE" >/dev/null

    echo "Kanly Admin is running at: http://localhost:$HOST_PORT"
    echo "Container: $CONTAINER"
    echo "Data dir: $DATA_DIR"
}

show_status() {
    docker ps --filter "name=^${CONTAINER}$" --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
}

case "$ACTION" in
    install)
        require_docker
        build_image
        run_container
        ;;
    build)
        require_docker
        build_image
        ;;
    start)
        require_docker
        run_container
        ;;
    restart)
        require_docker
        remove_container_if_exists
        run_container
        ;;
    stop)
        require_docker
        if docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
            docker stop "$CONTAINER" >/dev/null
            echo "Stopped: $CONTAINER"
        else
            echo "Container is not running: $CONTAINER"
        fi
        ;;
    logs)
        require_docker
        docker logs -f "$CONTAINER"
        ;;
    status)
        require_docker
        show_status
        ;;
    *)
        cat <<EOF
Usage: ./setup-docker.sh [install|build|start|restart|stop|status|logs]

Environment variables:
  KANLY_IMAGE       Docker image tag (default: kanly-admin:local)
  KANLY_CONTAINER   Container name (default: kanly-admin)
  KANLY_PORT        Host port for UI/API (default: 60000)
  KANLY_DUNE_ROOT   Path to dune-awakening-selfhost-docker (required path)
  KANLY_DATA_DIR    Persistent data dir for DB/session data (default: ./ .kanly-data)
EOF
        exit 1
        ;;
esac
