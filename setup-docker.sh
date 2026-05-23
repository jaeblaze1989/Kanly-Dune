#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

ACTION="${1:-install}"
IMAGE="${KANLY_IMAGE:-kanly-admin:local}"
CONTAINER="${KANLY_CONTAINER:-kanly-admin}"
HOST_PORT="${KANLY_PORT:-60000}"
CONTAINER_PORT=60000
DEFAULT_DUNE_ROOT="/srv/kanly/server/dune-awakening-selfhost-docker"
DUNE_ROOT="${KANLY_DUNE_ROOT:-$DEFAULT_DUNE_ROOT}"
DATA_DIR="${KANLY_DATA_DIR:-$PWD/.kanly-data}"

require_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        echo "docker is required but not installed." >&2
        exit 1
    fi
}

require_git() {
    if ! command -v git >/dev/null 2>&1; then
        echo "git is required for update but not installed." >&2
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

expand_path() {
    local input="$1"
    if [[ "$input" == ~* ]]; then
        printf '%s' "${input/#\~/$HOME}"
        return
    fi
    printf '%s' "$input"
}

prompt_for_dune_root_if_needed() {
    local explicit_root="${KANLY_DUNE_ROOT:-}"
    if [[ -n "$explicit_root" ]]; then
        DUNE_ROOT="$(expand_path "$explicit_root")"
        return
    fi

    # Prompt only for interactive shells; non-interactive runs keep defaults/env behavior.
    if [[ -t 0 ]]; then
        echo
        echo "Enter the full path to dune-awakening-selfhost-docker."
        echo "Press Enter to use default: $DEFAULT_DUNE_ROOT"
        read -r -p "Dune folder path: " input_path
        input_path="${input_path:-$DEFAULT_DUNE_ROOT}"
        DUNE_ROOT="$(expand_path "$input_path")"
    fi
}

self_update_from_git() {
    require_git

    local repo_root
    if ! repo_root="$(git rev-parse --show-toplevel 2>/dev/null)"; then
        echo "Update requires running inside a git checkout." >&2
        exit 1
    fi

    if [[ -n "$(git -C "$repo_root" status --porcelain 2>/dev/null)" ]]; then
        echo "Refusing to update because the repository has uncommitted changes:" >&2
        git -C "$repo_root" status --short >&2 || true
        echo "Commit or stash changes, then run ./setup-docker.sh update again." >&2
        exit 1
    fi

    local current_branch
    current_branch="$(git -C "$repo_root" rev-parse --abbrev-ref HEAD)"
    if [[ "$current_branch" == "HEAD" ]]; then
        echo "Detached HEAD detected. Checkout a branch before running update." >&2
        exit 1
    fi

    echo "Updating repo at: $repo_root"
    git -C "$repo_root" fetch --prune
    git -C "$repo_root" pull --ff-only origin "$current_branch"
}

run_container() {
    mkdir -p "$DATA_DIR"
    prompt_for_dune_root_if_needed

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
        -e KANLY_REPO_DIR=/kanly-repo \
        -v "$DATA_DIR:/app/data" \
        -v "$PWD:/kanly-repo:ro" \
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
    update)
        require_docker
        self_update_from_git
        build_image
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
Usage: ./setup-docker.sh [install|build|start|restart|update|stop|status|logs]

Environment variables:
  KANLY_IMAGE       Docker image tag (default: kanly-admin:local)
  KANLY_CONTAINER   Container name (default: kanly-admin)
  KANLY_PORT        Host port for UI/API (default: 60000)
  KANLY_DUNE_ROOT   Path to dune-awakening-selfhost-docker (required path)
  KANLY_DATA_DIR    Persistent data dir for DB/session data (default: ./ .kanly-data)

Notes:
    update            Pull latest git changes (fast-forward only), rebuild image, and restart container.
EOF
        exit 1
        ;;
esac
