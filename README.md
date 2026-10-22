# Hardened Containerized SFTPGo

[![CI](https://github.com/underhax/sftpgo-docker/actions/workflows/ci.yml/badge.svg)](https://github.com/underhax/sftpgo-docker/actions/workflows/ci.yml)
[![Build](https://github.com/underhax/sftpgo-docker/actions/workflows/build.yml/badge.svg)](https://github.com/underhax/sftpgo-docker/actions/workflows/build.yml)
[![GitHub last commit](https://img.shields.io/github/last-commit/underhax/sftpgo-docker)](https://github.com/underhax/sftpgo-docker/commits/main)
[![GitHub issues](https://img.shields.io/github/issues/underhax/sftpgo-docker)](https://github.com/underhax/sftpgo-docker/issues)
[![GitHub repo size](https://img.shields.io/github/repo-size/underhax/sftpgo-docker)](https://github.com/underhax/sftpgo-docker)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Hardened, containerized deployment architecture for [SFTPGo](https://github.com/drakkan/sftpgo) packaged in a minimal `scratch` runtime with PostgreSQL storage over an isolated `tmpfs` UNIX domain socket, host Discretionary Access Control (DAC) boundaries, in-memory secret mediation, zero configuration files, and an embedded static healthcheck probe.

## Architecture Highlights

While upstream SFTPGo provides minimal container images, running them securely without configuration files, exposed database ports, or external tooling requires additional orchestration. This project re-packages upstream SFTPGo into a hardened deployment:

* Custom scratch assembly: We extract the upstream binary and web assets but assemble our own scratch image with two static Go helpers: `init` as PID 1 to mediate Docker secrets into memory, and `probe` to provide container healthchecks without needing a shell or `curl`.
* Zero configuration files: Upstream relies on an `sftpgo.json` file on disk or plaintext environment variables. Here, all configuration and secrets are injected directly into process memory at startup.
* Socket-only database: Unlike standard setups that expose database ports across a Docker bridge network, PostgreSQL runs with container networking disabled entirely, communicating exclusively over an isolated in-memory UNIX socket.
* Isolated administration: WebAdmin and WebClient are split across separate listeners. The public reverse proxy reaches only the user WebClient, while the admin console is bound strictly to localhost for SSH tunnel or VPN access.
* Hardened execution: Containers run under an arbitrary unprivileged host user with read-only root filesystems, dropped Linux capabilities, and disabled privilege escalation.

---

<details>
<summary><strong>Directory Structure</strong></summary>

Production directory layout on the host (`/home/sftpgo`):

```text
├── docker-compose.yaml                # Production deployment manifest
├── .env                               # Host infrastructure variables (APP_UID, APP_GID, ports, versions)
├── secrets/                           # Docker secrets directory
│   ├── db_name.txt                    # PostgreSQL database name
│   ├── db_user.txt                    # PostgreSQL username
│   ├── db_password.txt                # PostgreSQL password
│   ├── sftpgo_admin_user.txt          # Optional: Initial admin username (bootstrap only)
│   ├── sftpgo_admin_password.txt      # Optional: Initial admin password (bootstrap only)
│   ├── sftpgo_kms_master_key.txt      # Optional: At-rest encryption master key
│   ├── sftpgo_oidc_config_url.txt     # Optional: OpenID Connect discovery URL
│   ├── sftpgo_oidc_client_id.txt      # Optional: OpenID Connect client ID
│   ├── sftpgo_oidc_client_secret.txt  # Optional: OpenID Connect client secret
│   ├── sftpgo_oidc_redirect_url.txt   # Optional: OpenID Connect redirect base URL
│   └── ssh_host_ed25519_key           # Optional: Ed25519 SSH host private key
├── postgres/                          # Persistent PostgreSQL storage
├── data/                              # User data directories
└── logs/                              # Application logs
```

</details>

---

<details>
<summary><strong>Deployment &amp; Setup</strong></summary>

### Prerequisites

* **Docker Engine & Compose:** Ensure Docker Engine and the Docker Compose plugin are installed on the host. Follow the official installation guide for [Ubuntu](https://docs.docker.com/engine/install/ubuntu/#install-using-the-repository).
* **External Gateway Network:** The stack communicates with the perimeter reverse proxy over an external Docker network named `frontend_gateway`. If utilizing the **[angie-docker-compose](https://github.com/webstudiobond/angie-docker-compose)** stack, this network is created automatically. Otherwise, create it manually:

  ```bash
  docker network create --subnet=172.20.0.0/16 frontend_gateway
  ```

### 1. Create a Dedicated System User

Choose a dedicated, unprivileged numeric ID unique to this service on your host and verify that it is not already in use by another user or group:

```bash
APP_UID=<CHOSEN_UID>

getent passwd ${APP_UID}
getent group ${APP_UID}
```

> **Important:** Both commands must return empty output. Each containerized service must have its own dedicated unprivileged user and group to enforce strict host DAC privilege separation between different stacks.

Create an unprivileged system account with a matching user-group (`-U`) and disabled login shell:

```bash
sudo useradd -m -d /home/sftpgo -s /usr/sbin/nologin -u ${APP_UID} -U sftpgo
```

### 2. Create Directory Layout & Assign Ownership

Create the base directory and assign ownership:

```bash
sudo mkdir -p /home/sftpgo
sudo chown root:sftpgo /home/sftpgo
sudo chmod 0750 /home/sftpgo
```

Create runtime subdirectories from `root` and assign strict DAC permissions:

```bash
sudo mkdir -p /home/sftpgo/{data,logs,postgres,secrets}

sudo chown root:sftpgo /home/sftpgo/secrets
sudo chmod 0750 /home/sftpgo/secrets

sudo chown sftpgo:sftpgo /home/sftpgo/logs
sudo chmod 0755 /home/sftpgo/logs

sudo chown sftpgo:sftpgo /home/sftpgo/{data,postgres}
sudo chmod 0700 /home/sftpgo/{data,postgres}
```

### 3. Generate Secrets

Install credential generation tools:

```bash
sudo apt update && sudo apt install -y pwgen openssl
```

**Database credentials:**

Initialize secret files with strict DAC permissions before writing:

```bash
sudo touch /home/sftpgo/secrets/{db_name,db_user,db_password}.txt
sudo chown root:sftpgo /home/sftpgo/secrets/{db_name,db_user,db_password}.txt
sudo chmod 0440 /home/sftpgo/secrets/{db_name,db_user,db_password}.txt

printf "db_%s" "$(pwgen -s -A 6 1)" | sudo tee /home/sftpgo/secrets/db_name.txt > /dev/null
printf "u_%s" "$(pwgen -s -A 6 1)" | sudo tee /home/sftpgo/secrets/db_user.txt > /dev/null
pwgen -s 64 1 | tr -d '\n' | sudo tee /home/sftpgo/secrets/db_password.txt > /dev/null
```

**Optional: Initial administrator credentials (bootstrap only):**

> **Note:** `sftpgo_admin_user.txt` and `sftpgo_admin_password.txt` are optional and utilized **strictly during the initial database bootstrap**. Once the administrator account is created in PostgreSQL, changing or deleting these secret files will have no effect on existing credentials. The administrator account can also be initialized interactively over the secure localhost WebAdmin binding (`http://127.0.0.1:54480/web/admin`) via an SSH tunnel or WireGuard.
>
> To use these secrets, uncomment `sftpgo_admin_user` and `sftpgo_admin_password` in `docker-compose.yaml` (both under `services.sftpgo.secrets` and the top-level `secrets:` block).

```bash
sudo touch /home/sftpgo/secrets/sftpgo_admin_{user,password}.txt
sudo chown root:sftpgo /home/sftpgo/secrets/sftpgo_admin_*.txt
sudo chmod 0440 /home/sftpgo/secrets/sftpgo_admin_*.txt

printf "admin_%s" "$(pwgen -s -A 6 1)" | sudo tee /home/sftpgo/secrets/sftpgo_admin_user.txt > /dev/null
pwgen -s 64 1 | tr -d '\n' | sudo tee /home/sftpgo/secrets/sftpgo_admin_password.txt > /dev/null
```

**Optional: KMS encryption master key:**

> **Note:** To enable KMS encryption, uncomment `sftpgo_kms_master_key` in `docker-compose.yaml` (both under `services.sftpgo.secrets` and the top-level `secrets:` block).

```bash
sudo touch /home/sftpgo/secrets/sftpgo_kms_master_key.txt
sudo chown root:sftpgo /home/sftpgo/secrets/sftpgo_kms_master_key.txt
sudo chmod 0440 /home/sftpgo/secrets/sftpgo_kms_master_key.txt

pwgen -s 64 1 | tr -d '\n' | sudo tee /home/sftpgo/secrets/sftpgo_kms_master_key.txt > /dev/null
```

**Optional: SSH host private key (for SFTP service):**

> **Note:** SFTP is disabled by default (`SFTPGO_SFTPD__BINDINGS__0__PORT=0`), eliminating unneeded host key requirements. If you want to enable the SFTP service:
> 1. Generate an Ed25519 host key using the command below.
> 2. In `/home/sftpgo/.env`, set `SFTPGO_SFTPD__BINDINGS__0__PORT=2022`, define `HOST_IP` with your host's external/public IP address, and choose a random unassigned high port for `SFTP_HOST_PORT` (avoid standard or predictable ports like 22 or 2022).
> 3. In `docker-compose.yaml`, uncomment the SFTP port mapping and the `ssh_host_ed25519_key` secret (under both `services.sftpgo.secrets` and the root `secrets:` block).

```bash
sudo ssh-keygen -t ed25519 -N "" -f /home/sftpgo/secrets/ssh_host_ed25519_key < /dev/null
sudo chown root:sftpgo /home/sftpgo/secrets/ssh_host_ed25519_key*
sudo chmod 0440 /home/sftpgo/secrets/ssh_host_ed25519_key*
```

**Lock secret files:**

```bash
sudo chown -R root:sftpgo /home/sftpgo/secrets
sudo chmod 0750 /home/sftpgo/secrets
sudo chmod 0440 /home/sftpgo/secrets/*
```

### 4. Deploy Project Files

Download the production manifests directly into `/home/sftpgo`:

```bash
REPO="https://raw.githubusercontent.com/underhax/sftpgo-docker/main"

sudo curl -fsSL ${REPO}/docker-compose.yaml -o /home/sftpgo/docker-compose.yaml
sudo curl -fsSL ${REPO}/.env.example -o /home/sftpgo/.env

sudo chown root:root /home/sftpgo/{docker-compose.yaml,.env}
sudo chmod 0600 /home/sftpgo/{docker-compose.yaml,.env}
```

### 5. Configure Host Environment Settings

Edit `/home/sftpgo/.env` using a text editor:

```bash
sudo nano /home/sftpgo/.env
```

Set the host parameters:
* `APP_UID` and `APP_GID`: Matching the numeric UID/GID of your dedicated host `sftpgo` account.
* `HOST_BIND_IP`: Host interface to bind. Defaults to `127.0.0.1` (loopback only) for private access via SSH tunnel. For WireGuard VPN, set this to the host's WireGuard interface IP (e.g. `10.8.0.1`).
* `HOST_PORT`: Unique unreserved host port (default `54480`, mapped to container internal port `8081`) for isolated WebAdmin access.

### 6. Start the Stack

Pull images and start the services:

```bash
sudo docker compose -f /home/sftpgo/docker-compose.yaml pull
sudo docker compose -f /home/sftpgo/docker-compose.yaml up -d
```

### 7. Private WebAdmin Access Recipes

The WebAdmin interface is bound strictly to `HOST_BIND_IP:HOST_PORT` (`127.0.0.1:54480`), completely inaccessible from public networks:

* **Access via SSH Tunnel (Default — 127.0.0.1):**
  Forward the unique host port securely over SSH:
  ```bash
  ssh -i ~/.ssh/id_ed25519 -p 22 -N -L 54480:127.0.0.1:54480 user@your-server-ip
  ```
  Then open `http://localhost:54480/web/admin` in your local browser to log in (or complete the initial administrator setup) and configure Two-Factor Authentication (2FA/TOTP).

* **Access via WireGuard VPN:**
  In `/home/sftpgo/.env`, set `HOST_BIND_IP` to your host's WireGuard interface address (e.g. `HOST_BIND_IP='10.8.0.1'`). Authorized VPN clients can then navigate directly to `http://10.8.0.1:54480/web/admin`.

</details>

---

<details>
<summary><strong>Operation &amp; Maintenance</strong></summary>

### Verifying Service Health

Inspect container status and resource usage:

```bash
sudo docker compose -f /home/sftpgo/docker-compose.yaml ps
sudo docker stats --no-stream sftpgo-app sftpgo-db
```

### Inspecting Runtime Logs

Follow application and database logs:

```bash
sudo docker compose -f /home/sftpgo/docker-compose.yaml logs -f
```

### Updating the Image

Pull updated images and recreate containers:

```bash
sudo docker compose -f /home/sftpgo/docker-compose.yaml pull
sudo docker compose -f /home/sftpgo/docker-compose.yaml up -d
```

### Tearing Down the Stack

Stop and remove containers and networks:

```bash
sudo docker compose -f /home/sftpgo/docker-compose.yaml down
```

</details>

---

<details>
<summary><strong>Development</strong></summary>

For local compilation, static analysis, unit testing, and Docker linting guidelines, see **[DEVELOPMENT.md](DEVELOPMENT.md)**.

</details>

---

<details>
<summary><strong>License &amp; Attribution</strong></summary>

## License & Attribution

This repository and deployment architecture are licensed under the [MIT License](LICENSE). This project is an independent containerized deployment architecture and is not affiliated with, endorsed, or sponsored by Nicola Murino.

**SFTPGo License & Ownership:** SFTPGo is open-source software licensed under the [GNU Affero General Public License v3.0](https://github.com/drakkan/sftpgo/blob/main/LICENSE) and developed by [Nicola Murino](https://github.com/drakkan).

</details>
