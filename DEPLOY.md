# Deployment Guide

This guide covers deploying get-iplayer-go to a remote server using Docker.

## Overview

The recommended workflow is:

1. Build the Docker image locally
2. Push it to a container registry
3. Pull and run it on the server via Docker Compose

The `deploy/deploy.sh` script handles steps 1 and 2. Step 3 is a one-time server setup.

---

## Local configuration

Create a `.env` file in the repo root (it is gitignored, so safe for personal config):

```bash
REGISTRY_IMAGE=your-registry.example.com/youruser/get-iplayer-go:latest
```

`deploy/deploy.sh` sources this file automatically if it exists, so you don't need to set `REGISTRY_IMAGE` in your shell each time.

---

## Build and push

```bash
./deploy/deploy.sh
```

This builds the image using `deploy/Dockerfile` with the repo root as the build context, tags it with `REGISTRY_IMAGE`, and pushes it to your registry.

---

## Server setup (one time)

`docker-compose.yml` references `${REGISTRY_IMAGE}` for the image name, so the server needs a `.env` alongside it with the same value you use locally. This tells Docker Compose where to pull the image from.

```bash
# Create the stack directory
ssh user@server 'mkdir -p /opt/stacks/get-iplayer-go/downloads'

# Copy the Compose file
scp deploy/docker-compose.yml user@server:/opt/stacks/get-iplayer-go/

# Create the .env so Compose knows which registry image to pull
ssh user@server 'echo "REGISTRY_IMAGE=your-registry.example.com/youruser/get-iplayer-go:latest" > /opt/stacks/get-iplayer-go/.env'
```

Then on the server:

```bash
cd /opt/stacks/get-iplayer-go
docker compose pull
docker compose up -d
```

Access the web UI at `http://your-server:7373`.

---

## Updating

From your local machine, build and push a new image:

```bash
./deploy/deploy.sh
```

Then on the server:

```bash
cd /opt/stacks/get-iplayer-go
docker compose pull
docker compose up -d
```

---

## Stack management

All commands run from `/opt/stacks/get-iplayer-go` on the server.

```bash
# View logs
docker compose logs -f

# View status
docker compose ps

# Restart
docker compose restart

# Stop
docker compose down

# Start
docker compose up -d
```

---

## Configuration

### Change port

Edit `/opt/stacks/get-iplayer-go/docker-compose.yml`:

```yaml
ports:
  - "3000:7373"
```

### Change downloads location

Edit `/opt/stacks/get-iplayer-go/docker-compose.yml`:

```yaml
volumes:
  - /your/custom/path:/app/downloads
```

Restart after any changes:

```bash
docker compose down && docker compose up -d
```

---

## Server directory layout

```
/opt/stacks/get-iplayer-go/
├── docker-compose.yml
├── .env                  # REGISTRY_IMAGE and any other local config
└── downloads/
    └── *.mp4
```

---

## Troubleshooting

**Container won't start** — check logs: `docker compose logs`

**Port already in use** — change the host port in `docker-compose.yml`

**Permission issues** — ensure correct ownership: `sudo chown -R $USER:$USER /opt/stacks/get-iplayer-go`

**Can't access web UI** — check the container is running (`docker compose ps`), the port is exposed (`docker port get-iplayer-go`), and your firewall allows port 7373

**Out of disk space** — clean up old images: `docker system prune -a`
