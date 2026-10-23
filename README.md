# Hardened Containerized SFTPGo

[![CI](https://github.com/underhax/sftpgo-docker/actions/workflows/ci.yml/badge.svg)](https://github.com/underhax/sftpgo-docker/actions/workflows/ci.yml)
[![Build](https://github.com/underhax/sftpgo-docker/actions/workflows/build.yml/badge.svg)](https://github.com/underhax/sftpgo-docker/actions/workflows/build.yml)
[![GitHub last commit](https://img.shields.io/github/last-commit/underhax/sftpgo-docker)](https://github.com/underhax/sftpgo-docker/commits/main)
[![GitHub issues](https://img.shields.io/github/issues/underhax/sftpgo-docker)](https://github.com/underhax/sftpgo-docker/issues)
[![GitHub repo size](https://img.shields.io/github/repo-size/underhax/sftpgo-docker)](https://github.com/underhax/sftpgo-docker)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Hardened, containerized deployment architecture for [SFTPGo](https://github.com/drakkan/sftpgo) packaged in a minimal `scratch` runtime with PostgreSQL storage over an isolated `tmpfs` UNIX domain socket, host Discretionary Access Control (DAC) boundaries, in-memory secret mediation, zero configuration files, and an embedded static healthcheck probe.

## Architecture Highlights

This deployment packages SFTPGo into an isolated, hardened runtime environment:

* Scratch container assembly: Built on a `scratch` base using the official SFTPGo binary, paired with two static Go helpers: `init` as PID 1 (maps secret files prefixed with `sftpgo_` directly into process memory as `SFTPGO_*` environment variables) and `probe` for HTTP healthchecks without external shells or dependencies.
* In-memory configuration: Service parameters and credentials are supplied to process memory via Docker secrets and environment variables, eliminating static configuration files on disk.
* Socket-only database: PostgreSQL runs with container networking disabled, communicating exclusively over an in-memory UNIX domain socket to prevent network-based lateral movement and eliminate TCP overhead.
* Interface separation: The user WebClient and administrative WebAdmin run on separate HTTP listeners. Only WebClient is routed through the reverse proxy; WebAdmin binds to localhost for access via SSH tunnel or WireGuard.
* Privilege separation: Containers run under a non-root UID/GID with read-only root filesystems, all Linux capabilities dropped, and privilege escalation disabled.

---

<details>
<summary><strong>Directory Structure</strong></summary>

Production directory layout on the host (`/home/sftpgo`):

```text
├── docker-compose.yaml                                        # Production deployment manifest
├── .env                                                       # Host infrastructure variables
├── secrets/                                                   # Docker secrets directory
│   ├── db_name.txt                                            # PostgreSQL database name
│   ├── db_user.txt                                            # PostgreSQL username
│   ├── db_password.txt                                        # PostgreSQL password
│   ├── sftpgo_default_admin_username.txt                      # Optional: Initial admin username (bootstrap only)
│   ├── sftpgo_default_admin_password.txt                      # Optional: Initial admin password (bootstrap only)
│   ├── sftpgo_kms__secrets__master_key.txt                    # Optional: At-rest encryption master key
│   ├── sftpgo_smtp__host.txt                                  # Optional: SMTP server host
│   ├── sftpgo_smtp__from.txt                                  # Optional: SMTP sender address
│   ├── sftpgo_smtp__user.txt                                  # Optional: SMTP login
│   ├── sftpgo_smtp__password.txt                              # Optional: SMTP password
│   ├── sftpgo_httpd__bindings__0__oidc__config_url.txt        # Optional: OpenID Connect discovery URL
│   ├── sftpgo_httpd__bindings__0__oidc__client_id.txt         # Optional: OpenID Connect client ID
│   ├── sftpgo_httpd__bindings__0__oidc__client_secret.txt     # Optional: OpenID Connect client secret
│   ├── sftpgo_httpd__bindings__0__oidc__redirect_base_url.txt # Optional: OpenID Connect redirect base URL
│   └── ssh_host_ed25519_key                                   # Optional: Ed25519 SSH host private key
├── postgres/                                                  # Persistent PostgreSQL storage
├── data/                                                      # User data directories
└── logs/                                                      # Application logs
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

sudo chown sftpgo:sftpgo /home/sftpgo/{data,logs,postgres}
sudo chmod 0700 /home/sftpgo/{data,logs,postgres}
```

### 3. Deploy Project Files

Download the production manifests directly into `/home/sftpgo`:

```bash
REPO="https://raw.githubusercontent.com/underhax/sftpgo-docker/main"

sudo curl -fsSL ${REPO}/docker-compose.yaml -o /home/sftpgo/docker-compose.yaml
sudo curl -fsSL ${REPO}/.env.example -o /home/sftpgo/.env

sudo chown root:root /home/sftpgo/{docker-compose.yaml,.env}
sudo chmod 0600 /home/sftpgo/{docker-compose.yaml,.env}
```

### 4. Configure Host Environment Settings

Edit `/home/sftpgo/.env` using a text editor:

```bash
sudo nano /home/sftpgo/.env
```

Set the host parameters:
* `APP_UID` and `APP_GID`: Matching the numeric UID/GID of your dedicated host `sftpgo` account.
* `HOST_BIND_IP`: Host interface to bind. Defaults to `127.0.0.1` (loopback only) for private access via SSH tunnel. For WireGuard VPN, set this to the host's WireGuard interface IP (e.g. `10.8.0.1`).
* `HOST_PORT`: Unique unreserved host port (default `54480`, mapped to container internal port `8081`) for isolated WebAdmin access.

### 5. Generate Secrets

> **Note:** Secret files mounted in `/run/secrets/` starting with `sftpgo_` are automatically converted to uppercase `SFTPGO_*` environment variables in process memory. Nested parameters use double underscores (for example, `sftpgo_smtp__password` maps to `SFTPGO_SMTP__PASSWORD`). This allows passing sensitive configuration without storing credentials in plaintext files.

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

> **Note:** `sftpgo_default_admin_username.txt` and `sftpgo_default_admin_password.txt` are optional and utilized **strictly during the initial database bootstrap**. Once the administrator account is created in PostgreSQL, changing or deleting these secret files will have no effect on existing credentials. The administrator account can also be initialized interactively over the secure localhost WebAdmin binding (`http://127.0.0.1:54480/portal/web/admin`) via an SSH tunnel or WireGuard.
>
> To use these secrets, uncomment `sftpgo_default_admin_username` and `sftpgo_default_admin_password` in `docker-compose.yaml` (both under `services.sftpgo.secrets` and the top-level `secrets:` block).

```bash
sudo touch /home/sftpgo/secrets/sftpgo_default_admin_{username,password}.txt
sudo chown root:sftpgo /home/sftpgo/secrets/sftpgo_default_admin_*.txt
sudo chmod 0440 /home/sftpgo/secrets/sftpgo_default_admin_*.txt

printf "admin_%s" "$(pwgen -s -A 6 1)" | sudo tee /home/sftpgo/secrets/sftpgo_default_admin_username.txt > /dev/null
pwgen -s 64 1 | tr -d '\n' | sudo tee /home/sftpgo/secrets/sftpgo_default_admin_password.txt > /dev/null

sudo nano /home/sftpgo/docker-compose.yaml
```

**Optional: KMS encryption master key:**

> **Note:** To enable KMS encryption, uncomment `sftpgo_kms__secrets__master_key` in `docker-compose.yaml` (both under `services.sftpgo.secrets` and the top-level `secrets:` block).

```bash
sudo touch /home/sftpgo/secrets/sftpgo_kms__secrets__master_key.txt
sudo chown root:sftpgo /home/sftpgo/secrets/sftpgo_kms__secrets__master_key.txt
sudo chmod 0440 /home/sftpgo/secrets/sftpgo_kms__secrets__master_key.txt

pwgen -s 64 1 | tr -d '\n' | sudo tee /home/sftpgo/secrets/sftpgo_kms__secrets__master_key.txt > /dev/null

sudo nano /home/sftpgo/docker-compose.yaml
```

**Optional: SMTP configuration (notifications & password resets):**

> **Note:** To enable SMTP delivery, uncomment `sftpgo_smtp__host`, `sftpgo_smtp__from`, `sftpgo_smtp__user`, and `sftpgo_smtp__password` in `docker-compose.yaml` (under both `services.sftpgo.secrets` and the root `secrets:` block). Populate the secret values using a text editor, and configure transport settings (`SFTPGO_SMTP__PORT`, `SFTPGO_SMTP__AUTH_TYPE`, `SFTPGO_SMTP__ENCRYPTION`) in `/home/sftpgo/.env`.

```bash
sudo touch /home/sftpgo/secrets/sftpgo_smtp__{host,from,user,password}.txt
sudo chown root:sftpgo /home/sftpgo/secrets/sftpgo_smtp__*.txt
sudo chmod 0440 /home/sftpgo/secrets/sftpgo_smtp__*.txt

sudo nano /home/sftpgo/secrets/sftpgo_smtp__host.txt
sudo nano /home/sftpgo/secrets/sftpgo_smtp__from.txt
sudo nano /home/sftpgo/secrets/sftpgo_smtp__user.txt
sudo nano /home/sftpgo/secrets/sftpgo_smtp__password.txt
sudo nano /home/sftpgo/docker-compose.yaml
sudo nano /home/sftpgo/.env
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

sudo nano /home/sftpgo/docker-compose.yaml
sudo nano /home/sftpgo/.env
```

**Lock secret files:**

```bash
sudo chown -R root:sftpgo /home/sftpgo/secrets
sudo chmod 0750 /home/sftpgo/secrets
sudo chmod 0440 /home/sftpgo/secrets/*
```

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
  Then open `http://127.0.0.1:54480/portal/web/admin` in your local browser to log in (or complete the initial administrator setup) and configure Two-Factor Authentication (2FA/TOTP).

* **Access via WireGuard VPN:**
  In `/home/sftpgo/.env`, set `HOST_BIND_IP` to your host's WireGuard interface address (e.g. `HOST_BIND_IP='10.8.0.1'`). Authorized VPN clients can then navigate directly to `http://10.8.0.1:54480/portal/web/admin`.

</details>

---

<details>
<summary><strong>External Angie (Perimeter Reverse Proxy)</strong></summary>

### Edge Reverse Proxy Configuration

The `sftpgo` container connects to the perimeter reverse proxy over the external `frontend_gateway` network (`172.20.0.0/16`) without publishing public HTTP or WebDAV ports on the host. An external Angie instance terminates TLS, handles automated Let's Encrypt certificates, manages HTTP/3 (QUIC), and streams large file transfers without disk buffering.

A production-hardened virtual host template [`sftpgo.conf`](https://github.com/webstudiobond/angie-docker-compose/blob/main/data/conf.d/domains/sftpgo.conf) is maintained in the **[angie-docker-compose](https://github.com/webstudiobond/angie-docker-compose)** repository. It provides subpath routing for WebClient (`/portal/`), WebDAV (`/dav/`), root perimeter lockdown (`return 404;`), and an optional commented WebAdmin block with geo-IP allowlisting.

Download the template into your Edge Angie domains directory:

```bash
DATA_ANGIE="/home/angie/data"
curl -fsSL "https://raw.githubusercontent.com/webstudiobond/angie-docker-compose/main/data/conf.d/domains/sftpgo.conf" \
  -o "${DATA_ANGIE}/conf.d/domains/sftpgo.conf"
```

### Parameter Synchronization Between Angie and SFTPGo

To ensure clean routing and prevent 404 errors, parameters in `/home/sftpgo/.env` must match the locations configured in `sftpgo.conf`.

#### 1. Web Interface Subpath (WebClient & WebAdmin)

In `/home/sftpgo/.env`, configure `SFTPGO_HTTPD__WEB_ROOT='/portal'`. In `sftpgo.conf`, this matches `location ^~ /portal/ { proxy_pass http://sftpgo_webclient; ... }`.

In production, you can replace `/portal/` with any unpredictable custom path in both files. In SFTPGo, `web_root` is an `httpd`-level setting that applies to both WebClient and WebAdmin. The root endpoint (`/`) returns 404 in Angie to prevent automated scanner discovery and enumeration.

#### 2. WebDAV Subpath

In `/home/sftpgo/.env`, configure `SFTPGO_WEBDAVD__BINDINGS__0__PREFIX='/dav'`. In `sftpgo.conf`, this matches `location ^~ /dav/ { proxy_pass http://sftpgo_webdav; ... }`.

In production, you can replace `/dav/` with any unpredictable custom path in both files.

#### 3. Reverse Proxy Real IP Trust

Reverse proxy trust and `X-Forwarded-For` client IP header extraction are pre-configured in `.env` for Angie's gateway (`172.20.0.254,172.20.0.1,127.0.0.1`) across both WebClient and WebDAV listeners, ensuring real client IPs are recorded in access logs and processed by Defender out of the box.

#### 4. Optional: Public WebAdmin Access

By default, WebAdmin is isolated on container port `8081` (`127.0.0.1:54480`) and accessed strictly via SSH tunnel or WireGuard.

To expose WebAdmin through the perimeter reverse proxy:
1. In `sftpgo.conf`, uncomment the `sftpgo_webadmin` upstream (`server sftpgo-app:8081 resolve;`).
2. Add your trusted IP addresses to the `geo $remote_addr $sftpgo_admin_allowed` block in `sftpgo.conf`.
3. Uncomment the `location ^~ /portal/web/admin` block in `sftpgo.conf`. All unauthorized IP addresses receive 404.

</details>

---

<details>
<summary><strong>Permissions &amp; Ownership</strong></summary>

## Host Permissions & Privilege Separation

The stack enforces strict host-level privilege separation across three ownership tiers so that even in the event of a container compromise (`sftpgo`), the process cannot modify deployment manifests, service configurations, or secret files:

* **`root:root` (Host-Only Manifests & Compose):**
  * `docker-compose.yaml`, `.env` — `0600`
* **`root:sftpgo` (Read-Only Service Configs, Secrets & Host Keys):**
  * `/home/sftpgo/` base directory — `0750`
  * `secrets/` — directory `0750`, secret files (`*.txt`, SSH host keys) `0440` (containers have group read-only access and cannot `chmod` or overwrite files)
* **`sftpgo:sftpgo` (Runtime Writable Storage):**
  * `data/` — `0700` (user data storage, managed with `umask 077`: directories `0700`, files `0600`)
  * `postgres/` — `0700` (PostgreSQL database cluster data)
  * `logs/` — `0700` (application log directory, log files `0600`)

### One-Shot Permission Reset (Running Stack)

Use this self-contained block to apply or restore canonical ownership and permissions on an existing or migrated installation without stopping the stack:

```bash
sudo chown root:sftpgo /home/sftpgo
sudo chmod 0750 /home/sftpgo

sudo chown root:root /home/sftpgo/docker-compose.yaml /home/sftpgo/.env
sudo chmod 0600 /home/sftpgo/docker-compose.yaml /home/sftpgo/.env

sudo chown -R root:sftpgo /home/sftpgo/secrets
sudo chmod 0750 /home/sftpgo/secrets
[ -d /home/sftpgo/secrets ] && sudo find /home/sftpgo/secrets -type f -exec chmod 0440 {} +

sudo chown -R sftpgo:sftpgo /home/sftpgo/{data,postgres,logs}
sudo chmod 0700 /home/sftpgo/{data,postgres,logs}
[ -d /home/sftpgo/data ] && sudo find /home/sftpgo/data -mindepth 1 -type d -exec chmod 0700 {} +
[ -d /home/sftpgo/data ] && sudo find /home/sftpgo/data -type f -exec chmod 0600 {} +
[ -d /home/sftpgo/logs ] && sudo find /home/sftpgo/logs -type f -exec chmod 0600 {} +
```

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
