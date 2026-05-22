#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

PORT="${PORT:-60000}"
MODE="${1:-restart}"
DEV_PID_FILE="dev.pid"
DEV_LOG_FILE="dev.log"

stop_process() {
    echo "Stopping kanly-admin on port $PORT..."

    if [ -f "$DEV_PID_FILE" ]; then
        pid=$(cat "$DEV_PID_FILE")
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid"
            echo "Stopped process $pid from $DEV_PID_FILE"
        fi
        rm -f "$DEV_PID_FILE"
    fi

    pids=""
    if command -v ss >/dev/null 2>&1; then
        pids=$(ss -H -ltnp 2>/dev/null | awk -v port=":$PORT" '$4 ~ port { print }' | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | sort -u)
    elif command -v lsof >/dev/null 2>&1; then
        pids=$(lsof -ti tcp:$PORT 2>/dev/null || true)
    fi

    if [ -z "$pids" ]; then
        echo "No process found on port $PORT."
        return
    fi

    echo "$pids" | xargs -r kill
    echo "Stopped $PORT listener(s): $pids"
}

build_css() {
    if [ ! -f package.json ]; then
        echo "No package.json found; skipping Tailwind CSS build."
        return 0
    fi

    echo "Building Tailwind CSS..."
    if command -v npm >/dev/null 2>&1; then
        npm run build:css
        return $?
    fi

    if command -v docker >/dev/null 2>&1; then
        docker run --rm -v "$PWD":/src -w /src node:20 bash -lc 'npm install && npm run build:css'
        return $?
    fi

    echo "Warning: npm and docker are not available; cannot build CSS." >&2
    return 1
}

start_process() {
    echo "Starting kanly-admin in development mode on port $PORT..."
    nohup env PORT="$PORT" KANLY_DEV=1 go run . > "$DEV_LOG_FILE" 2>&1 &
    pid=$!
    echo "$pid" > "$DEV_PID_FILE"
    if ! disown "$pid" 2>/dev/null; then
        true
    fi

    # Ensure startup succeeded (for example, catch bind failures).
    sleep 1
    if ! kill -0 "$pid" 2>/dev/null; then
        echo "kanly-admin failed to start; recent log output:" >&2
        tail -n 40 "$DEV_LOG_FILE" >&2 || true
        rm -f "$DEV_PID_FILE"
        exit 1
    fi

    echo "Started pid $pid; logs -> $DEV_LOG_FILE"
}

case "$MODE" in
    stop)
        stop_process
        ;;
    start)
        stop_process
        start_process
        ;;
    restart)
        stop_process
        build_css
        start_process
        ;;
    *)
        echo "Usage: $0 [start|stop|restart]"
        exit 1
        ;;
 esac
