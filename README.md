# Kanly Manager (User Guide)

Kanly Manager is a web control panel for your Dune self-host server stack.

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

## Install (Recommended)

Open a terminal in this folder and run:

```bash
chmod +x setup-docker.sh
./setup-docker.sh install
```

Then open:

```text
http://localhost:60000
```

If you want access from outside this machine/network, open TCP port `60000` in your firewall/router (or whatever custom port you set with `KANLY_PORT`).

On first launch, create your admin account and sign in.

## Install From GitHub

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

## Daily Use

Useful commands:

```bash
./setup-docker.sh status
./setup-docker.sh logs
./setup-docker.sh restart
./setup-docker.sh stop
./setup-docker.sh start
```

## If Your Dune Folder Is In A Different Location

By default, setup expects:

```text
/srv/kanly/server/dune-awakening-selfhost-docker
```

If yours is different, run install like this:

```bash
KANLY_DUNE_ROOT=/your/path/to/dune-awakening-selfhost-docker ./setup-docker.sh install
```

## If Port 60000 Is Already In Use

Use a different port:

```bash
KANLY_PORT=61000 ./setup-docker.sh install
```

Then open `http://localhost:61000`.

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
