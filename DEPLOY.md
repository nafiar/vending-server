# Deploying to the GCP VM

One command from your Mac sets everything up on the VM: the database, the server as a service, HTTPS on the public IP, and the demo data. This page explains what that command does, so you can also do each step by hand.

## What runs where

```
Browser / Kiosk Agent
        │  https://35.219.98.32        (port 443; port 80 only redirects to https)
        ▼
GCP firewall ── lets 80 and 443 through, blocks everything else
        ▼
VM ─ Caddy (ports 80, 443) ── holds the HTTPS certificate and forwards each request to
     └─ vending-server (127.0.0.1:8080) ── reachable only from inside the VM
        └─ MySQL or MariaDB (127.0.0.1:3306) ── reachable only from inside the VM
```

**Caddy** is a web server whose main trick is automatic HTTPS: it gets a certificate from Let's Encrypt by itself, renews it before it expires, and redirects `http://` to `https://`. Here it does only that and passes every request on to the Go server. That job is called a *reverse proxy*. nginx can do the same, but needs certbot and a renewal timer next to it for the certificate; Caddy needs one short config file.

Let's Encrypt certificates for a bare IP address (no domain) are valid for 6 days, and Caddy renews them automatically.

## Step 1 — Open ports 80 and 443 in GCP (once)

**Console:** Compute Engine → VM instances → click your VM → **Edit** → under *Firewalls*, tick **Allow HTTP traffic** and **Allow HTTPS traffic** → **Save**.

This attaches the `http-server` and `https-server` tags to the VM, which the `default` network's built-in rules allow. Port 443 is HTTPS. Port 80 is needed too, because Let's Encrypt checks it before issuing the certificate and Caddy uses it to redirect to HTTPS. Don't open 8080 or 3306.

**Or with gcloud**, from your Mac:

```bash
gcloud compute instances add-tags VM_NAME --zone ZONE --tags http-server,https-server
# only if the default rules don't exist in your project:
gcloud compute firewall-rules create allow-web --allow tcp:80,tcp:443 --target-tags http-server,https-server
```

## Step 2 — Check that ssh works from your Mac

```bash
ssh YOUR_USER@35.219.98.32 'sudo whoami'      # must print: root
```

If you usually connect with `gcloud compute ssh`, run `gcloud compute config-ssh` once. It writes an alias such as `VM_NAME.ZONE.PROJECT` into `~/.ssh/config`, and plain `ssh` (which the script uses) then works with that alias.

## Step 3 — Install

From the project folder on your Mac:

```bash
./deploy/deploy.sh YOUR_USER@35.219.98.32 install
```

It builds the Linux binary on your Mac, copies it to the VM, and runs `deploy/setup-vm.sh` there with sudo. It takes about five minutes the first time. At the end it prints the demo logins (`admin / admin123`, …) and the three machine keys, then checks that `https://35.219.98.32` works.

Running it again is safe: every step checks what is already there, and the database password, the data and the machine keys are kept.

## Daily use

| You want to | Run on your Mac |
| --- | --- |
| Put new code on the server | `./deploy/deploy.sh YOUR_USER@35.219.98.32 update` |
| Wipe everything and start from the demo data | `./deploy/deploy.sh YOUR_USER@35.219.98.32 reset`, or the **Reset data demo** button in the admin web |
| See the logins and machine keys again | `./deploy/deploy.sh YOUR_USER@35.219.98.32 keys` |
| Check that it is running | `./deploy/deploy.sh YOUR_USER@35.219.98.32 status` |
| Watch the server log | `./deploy/deploy.sh YOUR_USER@35.219.98.32 logs` (Ctrl+C to stop) |

**Reset** deletes every transaction, visit, product, machine and account, then loads the demo data again. The machine keys of VM-01 to VM-03 stay the same, so a kiosk you already configured keeps working. Everyone is logged out; log in again with `admin / admin123`.

On the VM itself, `sudo vending <command>` runs any server command with the right settings, for example `sudo vending reconcile` or `sudo vending create-admin -username nafiar`.

## When the IP changes

An ephemeral IP changes when the VM is stopped and started. Run `install` again with the new IP: Caddy gets a certificate for it, and the data is untouched. Then put the new address in the kiosk's configuration.

## If HTTPS doesn't come up

- **Ports 80 or 443 closed**: this is the usual cause; recheck step 1. Caddy keeps retrying by itself, so check again with `status` a minute after fixing it.
- **The certificate error itself**: see it with `ssh YOUR_USER@35.219.98.32 'sudo journalctl -u caddy -n 50'`.
- **Let's Encrypt keeps refusing the IP**: switch to a self-signed certificate with `./deploy/deploy.sh YOUR_USER@35.219.98.32 install --self-signed`. Browsers will show a warning once, and the Kiosk Agent must then skip certificate verification. Running `install` without the flag goes back to Let's Encrypt.

---

## What the script does, step by step

If you want to do it by hand, or just see what it does, here is everything `setup-vm.sh install` runs on the VM. Run these as root (`sudo -i`).

### 1. Database

Ubuntu images get MySQL 8.0; Debian images (GCP's default) get MariaDB, which this server also supports. Either one listens only on 127.0.0.1 out of the box.

```bash
apt-get update
apt-get install -y mariadb-server        # on Ubuntu: apt-get install -y mysql-server
systemctl enable --now mariadb           # on Ubuntu: systemctl enable --now mysql
```

### 2. The app's database user

As root on the VM, `mysql` logs you in as the database's root user without a password (socket authentication). Generate a password, then create the database and a user that can use only that database:

```bash
DB_PASSWORD=$(openssl rand -hex 16)
echo "$DB_PASSWORD"                      # goes into the settings file in step 3

mysql <<SQL
CREATE DATABASE IF NOT EXISTS vending CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'vending'@'localhost' IDENTIFIED BY '$DB_PASSWORD';
CREATE USER IF NOT EXISTS 'vending'@'127.0.0.1' IDENTIFIED BY '$DB_PASSWORD';
GRANT ALL PRIVILEGES ON vending.* TO 'vending'@'localhost';
GRANT ALL PRIVILEGES ON vending.* TO 'vending'@'127.0.0.1';
FLUSH PRIVILEGES;
SQL

mysql -uvending -p"$DB_PASSWORD" -h127.0.0.1 -e 'SHOW DATABASES'   # check: shows vending
```

There are two user rows because the server connects over TCP to 127.0.0.1, while the `mysql` command line connects through the local socket as `localhost`; MySQL treats those as different hosts. `ALL PRIVILEGES ON vending.*` lets the server create its tables, and lets a reset empty them, without any rights on other databases.

### 3. Program, settings, folders

```bash
useradd --system --home-dir /var/lib/vending --shell /usr/sbin/nologin vending
install -d -m 755 /opt/vending
install -d -m 750 -o root -g vending /etc/vending
install -d -m 750 -o vending -g vending /var/lib/vending /var/lib/vending/uploads
install -m 755 /tmp/vending-server /opt/vending/vending-server
```

`/etc/vending/vending.env`, readable by root and the `vending` group only (`chmod 640`):

```
DB_HOST=127.0.0.1
DB_PORT=3306
DB_USER=vending
DB_PASSWORD=<the generated password>
DB_NAME=vending
HTTP_ADDR=127.0.0.1:8080
TIMEZONE=Asia/Jakarta
UPLOAD_DIR=/var/lib/vending/uploads
AUTO_MIGRATE=true
DEMO_MODE=true
LOG_FORMAT=text
LOG_LEVEL=info
```

`HTTP_ADDR=127.0.0.1:8080` keeps the Go server off the internet; only Caddy reaches it. `DEMO_MODE=true` shows the reset button in the admin web.

The file is also linked as `/var/lib/vending/.env`, so commands run by hand read the same settings. The shortcut `/usr/local/bin/vending` runs them as the `vending` user from that folder.

### 4. The service

`/etc/systemd/system/vending.service`:

```ini
[Unit]
Description=Vending machine information system (admin web and API)
After=network-online.target mysql.service mariadb.service
Wants=network-online.target

[Service]
User=vending
Group=vending
WorkingDirectory=/var/lib/vending
EnvironmentFile=/etc/vending/vending.env
ExecStart=/opt/vending/vending-server serve
Restart=always
RestartSec=3
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now vending           # start now and at every boot
systemctl status vending                 # is it running?
journalctl -u vending -f                 # its log, live
```

**Logs need no change in the program.** It writes its log to stderr, and systemd stores everything a service writes in its journal; `journalctl` reads it back with timestamps. `LOG_FORMAT=text` is the easiest to read there. `LOG_FORMAT=json` gives one JSON object per line, for a log collector or for `journalctl -u vending -o cat | jq` (change the setting, then `systemctl restart vending`).

### 5. Demo data

```bash
sudo vending seed-demo | tee /etc/vending/demo-keys.txt
```

### 6. Caddy and HTTPS

Install from the official Caddy repository (Debian's own `caddy` package is too old for IP certificates):

```bash
apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl gnupg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' > /etc/apt/sources.list.d/caddy-stable.list
apt-get update && apt-get install -y caddy
```

`/etc/caddy/Caddyfile`:

```
{
	default_sni 35.219.98.32
}

https://35.219.98.32 {
	tls {
		issuer acme {
			profile shortlived
		}
	}
	encode zstd gzip
	reverse_proxy 127.0.0.1:8080
}
```

```bash
systemctl restart caddy
journalctl -u caddy -f                   # wait for "certificate obtained successfully"
```

- **`profile shortlived`** asks Let's Encrypt for the 6-day certificate type, the only one it issues for IP addresses.
- **`default_sni`** is needed because a browser that connects to a bare IP doesn't send a server name, and Caddy uses it to pick the right certificate.
- **The redirect** from port 80 to HTTPS is automatic.
- **For a self-signed certificate**, replace the whole `tls { … }` block with `tls internal`.
