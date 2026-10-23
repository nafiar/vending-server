#!/usr/bin/env bash
# setup-vm.sh — install, update or reset the vending server on a Debian or Ubuntu VM.
# deploy.sh copies this file and the binary to /tmp on the VM and runs it with sudo.
# Every step checks what is already there, so running it again is safe.
#
#   sudo bash setup-vm.sh install [--self-signed]   database, user, service, HTTPS, demo data
#   sudo bash setup-vm.sh update                    replace the binary and restart
#   sudo bash setup-vm.sh reset                     delete all data, load the demo data again
#   sudo bash setup-vm.sh keys | status | logs
#
# Layout on the VM:
#   /opt/vending/vending-server     the program
#   /etc/vending/vending.env        settings (database password inside), also read as /var/lib/vending/.env
#   /etc/vending/demo-keys.txt      demo logins and machine keys printed by the first seed
#   /var/lib/vending/uploads        product images
#   /etc/caddy/Caddyfile            HTTPS on the public IP, forwarding to 127.0.0.1:8080
#   /usr/local/bin/vending          shortcut: sudo vending reset | reconcile | create-admin ...
set -euo pipefail

APP_USER=vending
APP_DIR=/opt/vending
BIN=$APP_DIR/vending-server
ETC_DIR=/etc/vending
ENV_FILE=$ETC_DIR/vending.env
KEYS_FILE=$ETC_DIR/demo-keys.txt
DATA_DIR=/var/lib/vending
NEW_BIN=${NEW_BIN:-/tmp/vending-server}
DB_NAME=vending
DB_USER=vending

step() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }
die() {
	printf '\033[31merror:\033[0m %s\n' "$*" >&2
	exit 1
}

require_root() {
	[[ $EUID -eq 0 ]] || die "run as root: sudo bash $0"
	command -v apt-get >/dev/null || die "this script supports Debian and Ubuntu (apt) only"
}

# The VM's external IP from the GCP metadata server, which also knows an ephemeral IP.
detect_public_ip() {
	local ip=""
	ip=$(curl -fsS -m 3 -H 'Metadata-Flavor: Google' \
		'http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip' 2>/dev/null || true)
	if [[ -z $ip ]]; then
		ip=$(curl -fsS -m 5 https://api.ipify.org 2>/dev/null || true)
	fi
	[[ $ip =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "could not find the public IP; pass it: PUBLIC_IP=1.2.3.4 sudo -E bash $0 install"
	echo "$ip"
}

install_base_packages() {
	step "Base packages"
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq
	apt-get install -y -qq curl ca-certificates gnupg openssl >/dev/null
	info "curl, ca-certificates, gnupg and openssl are installed"
}

# Ubuntu ships MySQL 8.0; Debian (GCP's default image) ships MariaDB. The server works on both.
install_database() {
	step "Database"
	if command -v mysqld >/dev/null 2>&1 || command -v mariadbd >/dev/null 2>&1; then
		info "a MySQL or MariaDB server is already installed; using it"
	else
		local pkg=mariadb-server
		# shellcheck disable=SC1091
		. /etc/os-release
		[[ ${ID:-} == ubuntu ]] && pkg=mysql-server
		info "installing $pkg"
		DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$pkg" >/dev/null
	fi
	systemctl enable --now mariadb >/dev/null 2>&1 || systemctl enable --now mysql >/dev/null 2>&1 || true
	for _ in $(seq 1 30); do
		mysql -e 'SELECT 1' >/dev/null 2>&1 && break
		sleep 1
	done
	mysql -e 'SELECT 1' >/dev/null 2>&1 || die "the database server is not answering; see: journalctl -u mysql -u mariadb -n 50"
	info "$(mysql -N -e 'SELECT VERSION()') is running and listens only on this VM"
}

# The app's own database user, with rights on its database only. The password is
# generated once and kept in the settings file, so a re-run keeps it.
setup_database_user() {
	step "Database user '$DB_USER' and database '$DB_NAME'"
	local pass
	if [[ -f $ENV_FILE ]] && grep -q '^DB_PASSWORD=' "$ENV_FILE"; then
		pass=$(grep '^DB_PASSWORD=' "$ENV_FILE" | cut -d= -f2-)
	else
		pass=$(openssl rand -hex 16) # hex only: safe in SQL and in the settings file
	fi
	mysql <<SQL
CREATE DATABASE IF NOT EXISTS \`$DB_NAME\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS '$DB_USER'@'localhost' IDENTIFIED BY '$pass';
CREATE USER IF NOT EXISTS '$DB_USER'@'127.0.0.1' IDENTIFIED BY '$pass';
ALTER USER '$DB_USER'@'localhost' IDENTIFIED BY '$pass';
ALTER USER '$DB_USER'@'127.0.0.1' IDENTIFIED BY '$pass';
GRANT ALL PRIVILEGES ON \`$DB_NAME\`.* TO '$DB_USER'@'localhost';
GRANT ALL PRIVILEGES ON \`$DB_NAME\`.* TO '$DB_USER'@'127.0.0.1';
FLUSH PRIVILEGES;
SQL
	DB_PASSWORD=$pass
	info "user $DB_USER@localhost can use database $DB_NAME only"
}

setup_app_user_and_files() {
	step "Program, settings and folders"
	id -u "$APP_USER" >/dev/null 2>&1 || useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$APP_USER"
	install -d -m 755 "$APP_DIR"
	install -d -m 750 -o root -g "$APP_USER" "$ETC_DIR"
	install -d -m 750 -o "$APP_USER" -g "$APP_USER" "$DATA_DIR" "$DATA_DIR/uploads"

	[[ -f $NEW_BIN ]] || die "the program is missing at $NEW_BIN; deploy.sh copies it there"
	install -m 755 "$NEW_BIN" "$BIN"
	info "installed $("$BIN" version)"

	if [[ ! -f $ENV_FILE ]]; then
		cat >"$ENV_FILE" <<EOF
# Settings of the vending server; written by setup-vm.sh. Restart after a change:
#   sudo systemctl restart vending
DB_HOST=127.0.0.1
DB_PORT=3306
DB_USER=$DB_USER
DB_PASSWORD=$DB_PASSWORD
DB_NAME=$DB_NAME
# Only Caddy talks to the program; the internet reaches it through HTTPS on 443.
HTTP_ADDR=127.0.0.1:8080
TIMEZONE=Asia/Jakarta
UPLOAD_DIR=$DATA_DIR/uploads
AUTO_MIGRATE=true
# Demo server: shows "Reset data demo" in the admin web.
DEMO_MODE=true
# text reads well in journalctl; json suits log collectors.
LOG_FORMAT=text
LOG_LEVEL=info
EOF
		info "wrote $ENV_FILE"
	else
		info "kept the existing $ENV_FILE"
	fi
	chown root:"$APP_USER" "$ENV_FILE"
	chmod 640 "$ENV_FILE"
	ln -sf "$ENV_FILE" "$DATA_DIR/.env" # the program also reads .env from its working folder

	cat >/usr/local/bin/vending <<EOF
#!/bin/sh
# Runs a vending-server command as the service user with the server's settings.
# Examples: sudo vending reset | sudo vending reconcile | sudo vending create-admin -username nafiar
cd $DATA_DIR && exec runuser -u $APP_USER -- $BIN "\$@"
EOF
	chmod 755 /usr/local/bin/vending
	info "shortcut: sudo vending <command>"
}

setup_service() {
	step "systemd service 'vending'"
	cat >/etc/systemd/system/vending.service <<EOF
[Unit]
Description=Vending machine information system (admin web and API)
After=network-online.target mysql.service mariadb.service
Wants=network-online.target

[Service]
User=$APP_USER
Group=$APP_USER
WorkingDirectory=$DATA_DIR
EnvironmentFile=$ENV_FILE
ExecStart=$BIN serve
# Restart after a crash, a reboot or a failed start (e.g. the database still starting).
Restart=always
RestartSec=3
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
	systemctl daemon-reload
	systemctl enable vending >/dev/null 2>&1
	info "the server starts at boot and restarts by itself after a crash"
}

seed_first_time() {
	step "Demo data"
	local users
	users=$(mysql -N "$DB_NAME" -e 'SELECT COUNT(*) FROM users' 2>/dev/null || echo 0)
	if [[ $users -gt 0 ]]; then
		info "the database already has accounts; left as it is (use 'reset' to start over)"
		return
	fi
	vending seed-demo | tee "$KEYS_FILE"
	chmod 600 "$KEYS_FILE"
	info "saved to $KEYS_FILE (show again with: deploy.sh <target> keys)"
}

install_caddy() {
	step "Caddy (HTTPS in front of the server)"
	if command -v caddy >/dev/null 2>&1; then
		info "already installed: $(caddy version | cut -d' ' -f1)"
		return
	fi
	# The official Caddy package repository (https://caddyserver.com/docs/install#debian-ubuntu-raspbian).
	# Debian's own caddy package is too old for IP-address certificates.
	DEBIAN_FRONTEND=noninteractive apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https >/dev/null
	curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --yes --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
	curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' >/etc/apt/sources.list.d/caddy-stable.list
	chmod o+r /usr/share/keyrings/caddy-stable-archive-keyring.gpg /etc/apt/sources.list.d/caddy-stable.list
	apt-get update -qq
	DEBIAN_FRONTEND=noninteractive apt-get install -y -qq caddy >/dev/null
	info "installed $(caddy version | cut -d' ' -f1)"
}

# HTTPS directly on the IP address: Let's Encrypt has issued 6-day certificates for
# IP addresses since January 2026; Caddy renews them by itself. Browsers send no
# server name when they connect to an IP, hence default_sni.
write_caddyfile() {
	local ip=$1 mode=$2 tls_block
	step "HTTPS on https://$ip"
	if [[ $mode == self-signed ]]; then
		tls_block="	tls internal"
		info "self-signed certificate: browsers will warn once; the kiosk must skip verification"
	else
		tls_block="	tls {
		issuer acme {
			profile shortlived
		}
	}"
		info "certificate from Let's Encrypt (6-day IP certificate, renewed automatically)"
	fi
	cat >/etc/caddy/Caddyfile <<EOF
# Written by setup-vm.sh. Caddy answers on ports 80 and 443 and passes every
# request to the vending server on 127.0.0.1:8080. Port 80 only redirects to
# HTTPS and answers Let's Encrypt's check.
{
	default_sni $ip
}

https://$ip {
$tls_block
	encode zstd gzip
	reverse_proxy 127.0.0.1:8080
}
EOF
	caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1 || die "the Caddyfile is invalid: caddy validate --config /etc/caddy/Caddyfile"
	if command -v ufw >/dev/null 2>&1 && ufw status | grep -q 'Status: active'; then
		ufw allow 80/tcp >/dev/null && ufw allow 443/tcp >/dev/null
		info "opened 80 and 443 in ufw"
	fi
	systemctl enable caddy >/dev/null 2>&1
	systemctl restart caddy
}

start_server() {
	step "Starting the server"
	systemctl restart vending
	for _ in $(seq 1 20); do
		curl -fsS -m 2 http://127.0.0.1:8080/api/health >/dev/null 2>&1 && break
		sleep 1
	done
	if curl -fsS -m 2 http://127.0.0.1:8080/api/health >/dev/null 2>&1; then
		info "the server answers on 127.0.0.1:8080"
	else
		journalctl -u vending -n 30 --no-pager || true
		die "the server did not start; the log is above"
	fi
}

# Checks HTTPS the way a browser outside would, but connects through this VM's
# own port 443 so it doesn't depend on the firewall letting the VM reach itself.
check_https() {
	local ip=$1 mode=$2
	step "Checking https://$ip"
	local flags=(-fsS -m 5 --noproxy "*" --connect-to "::127.0.0.1:")
	[[ $mode == self-signed ]] && flags+=(-k)
	for i in $(seq 1 45); do
		if curl "${flags[@]}" "https://$ip/api/health" >/dev/null 2>&1; then
			info "HTTPS works: open https://$ip in a browser"
			return 0
		fi
		[[ $i -eq 1 ]] && info "waiting for the certificate (Let's Encrypt checks this VM from the internet)..."
		sleep 2
	done
	printf '\n'
	journalctl -u caddy -n 25 --no-pager | grep -iE 'error|obtain|challenge|certificate' || true
	cat <<EOF

    No valid certificate yet. Most often port 80 or 443 is closed in the GCP firewall
    (VM > Edit > Firewalls: tick "Allow HTTP traffic" and "Allow HTTPS traffic").
    Caddy keeps retrying by itself; check again with: deploy.sh <target> status
    If Let's Encrypt keeps refusing, switch to a self-signed certificate:
        deploy.sh <target> install --self-signed
EOF
	return 1
}

show_summary() {
	local ip=$1
	step "Done"
	info "Admin web:   https://$ip"
	info "Kiosk API:   https://$ip/api/machine   (header X-Machine-Key)"
	[[ -f $KEYS_FILE ]] && info "Logins and machine keys: deploy.sh <target> keys"
	info "Logs:        deploy.sh <target> logs"
	info "Reset data:  deploy.sh <target> reset   (or the 'Reset data demo' button in the admin web)"
}

cmd_install() {
	local mode=letsencrypt
	[[ ${1:-} == --self-signed ]] && mode=self-signed
	local ip=${PUBLIC_IP:-}
	install_base_packages
	[[ -n $ip ]] || ip=$(detect_public_ip)
	install_database
	setup_database_user
	setup_app_user_and_files
	setup_service
	seed_first_time
	start_server
	install_caddy
	write_caddyfile "$ip" "$mode"
	check_https "$ip" "$mode" || true
	show_summary "$ip"
}

cmd_update() {
	[[ -f $BIN ]] || die "not installed yet; run: deploy.sh <target> install"
	step "Updating the program"
	install -m 755 "$NEW_BIN" "$BIN"
	info "installed $("$BIN" version)"
	start_server
}

cmd_reset() {
	step "Resetting all data"
	vending reset
	info "every session ended: log in again with admin / admin123"
}

cmd_status() {
	systemctl --no-pager --lines=0 status vending caddy || true
	printf '\nHealth: '
	curl -fsS -m 3 http://127.0.0.1:8080/api/health || printf 'not answering'
	printf '\n'
}

main() {
	local cmd=${1:-}
	shift || true
	case $cmd in
	install) require_root && cmd_install "$@" ;;
	update) require_root && cmd_update ;;
	reset) require_root && cmd_reset ;;
	keys) require_root && cat "$KEYS_FILE" ;;
	status) require_root && cmd_status ;;
	logs) require_root && journalctl -u vending -n 100 -f ;;
	*) die "usage: sudo bash $0 install [--self-signed] | update | reset | keys | status | logs" ;;
	esac
}

# Run main unless the file is sourced (the tests source it to call single steps).
if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	main "$@"
fi
