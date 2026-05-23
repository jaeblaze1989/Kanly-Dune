# Kanly Manager (User Guide)

Kanly Manager is a web control panel for your Dune self-host server stack.

Current release: `v1.2.0`

Required Dune package:

- https://github.com/Red-Blink/dune-awakening-selfhost-docker

It gives you one place to:

1. Check system health and container status.
2. View player information.
3. Edit map INI files.
4. Run server commands safely from the UI.

## Before You Start

You need:

1. Docker installed and running.
2. A working Dune self-host package folder on this machine.
3. Terminal access on the same host where Dune is running.

## Install From GitHub (Recommended)

If you are starting from scratch:

```bash
git clone https://github.com/jaeblaze1989/Kanly-Dune.git
cd Kanly-Dune/services/kanly-admin
chmod +x setup-docker.sh
./setup-docker.sh install
```

Then open:

```text
http://localhost:60000
```

If you want access from outside this machine/network, open TCP port `60000` in your firewall/router.

On first launch, create your admin account and sign in.

## Local Install (Existing Checkout)

If you already have this repository on the host, open a terminal in this folder and run:

```bash
chmod +x setup-docker.sh
./setup-docker.sh install
```

Then open:

```text
http://localhost:60000
```

Or pull and run the prebuilt image directly:

```bash
docker pull ghcr.io/jaeblaze1989/kanly-manager:sha-55098c9
```

```bash
docker run -d \
	--name kanly-admin \
	--restart unless-stopped \
	-p 60000:60000 \
	-e PORT=60000 \
	-e KANLY_DUNE_ROOT=/dune \
	-e KANLY_DB_PATH=/app/data/kanly.db \
	-v "$PWD/.kanly-data:/app/data" \
	-v "/srv/kanly/server/dune-awakening-selfhost-docker:/dune" \
	-v /var/run/docker.sock:/var/run/docker.sock \
	ghcr.io/jaeblaze1989/kanly-manager:sha-55098c9
```

Open:

```text
http://localhost:60000
```

Helpful commands for this mode:

```bash
docker logs -f kanly-admin
docker stop kanly-admin
docker start kanly-admin
docker rm -f kanly-admin
```

## Daily Use

Useful commands:

```bash
./setup-docker.sh status
./setup-docker.sh logs
./setup-docker.sh restart
./setup-docker.sh update
./setup-docker.sh stop
./setup-docker.sh start
```

## Update From Git

To self-update Kanly Manager from your current Git branch and redeploy:

```bash
./setup-docker.sh update
```

What this does:

1. Verifies the checkout is clean (no uncommitted changes).
2. Runs a fast-forward `git pull` from `origin` on the current branch.
3. Rebuilds the Docker image.
4. Restarts the `kanly-admin` container.

If your repo has local changes, the update command stops and asks you to commit or stash first.

## Dune Folder Path

By default, setup expects:

```text
/srv/kanly/server/dune-awakening-selfhost-docker
```

If yours is different, run install like this:

```bash
KANLY_DUNE_ROOT=/your/path/to/dune-awakening-selfhost-docker ./setup-docker.sh install
```

If `KANLY_DUNE_ROOT` is not set, `./setup-docker.sh install` now prompts for the folder path interactively.

## Where Your Kanly Data Is Stored

Kanly Manager stores its local data in:

```text
./.kanly-data
```

Keep this folder backed up if you want to preserve local manager state.

## Troubleshooting

1. Manager does not start:
- Run `./setup-docker.sh logs` and check errors.

2. UI loads but cannot control services:
- Confirm Docker is running.
- Confirm Dune folder path is correct (`KANLY_DUNE_ROOT`).

3. Cannot open webpage:
- Check `./setup-docker.sh status`.
- Confirm firewall allows your chosen port.

## GitHub Note

You can store this project on GitHub, but Kanly Manager itself cannot run on GitHub Pages (it needs a backend service and Docker access).

Use GitHub Pages for documentation only, and run Kanly Manager on your own server/host.

## License

See [LICENSE.md](LICENSE.md).
