#!/usr/bin/env bash
# deploy.sh — build the server on your laptop and set it up on the VM, in one command.
#
#   ./deploy/deploy.sh <ssh-target> install [--self-signed]   first time (safe to repeat, e.g. after the IP changed)
#   ./deploy/deploy.sh <ssh-target> update                    new code: build, copy, restart
#   ./deploy/deploy.sh <ssh-target> reset                     delete all data, load the demo data again
#   ./deploy/deploy.sh <ssh-target> keys                      demo logins and machine keys
#   ./deploy/deploy.sh <ssh-target> status                    is everything running?
#   ./deploy/deploy.sh <ssh-target> logs                      follow the server log (Ctrl+C to stop)
#
# If MySQL's root uses a password: DB_ROOT_PASSWORD='...' ./deploy/deploy.sh <ssh-target> install
# <ssh-target> is what you type after "ssh", e.g. nafiar@35.219.98.32 or a ~/.ssh/config alias.
# Needs Go and ssh on this computer; the VM needs nothing but sudo.
# Works with the bash that ships with macOS.
set -euo pipefail

usage() {
	sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
	exit 1
}

[ $# -ge 2 ] || usage
TARGET=$1
CMD=$2
shift 2

ROOT=$(cd "$(dirname "$0")/.." && pwd)
SSH_OPTS=(-o ConnectTimeout=15)

remote() { ssh "${SSH_OPTS[@]}" -t "$TARGET" "$@"; }

# DB_ROOT_PASSWORD (and DB_ROOT_USER) on this computer are passed to the VM, for a
# MySQL whose root logs in with a password instead of the Unix socket.
ENV_PASS=""
if [ -n "${DB_ROOT_PASSWORD:-}" ]; then
	ENV_PASS="DB_ROOT_PASSWORD=$(printf %q "$DB_ROOT_PASSWORD") DB_ROOT_USER=$(printf %q "${DB_ROOT_USER:-root}")"
fi

build_and_copy() {
	command -v go >/dev/null || {
		echo "Go is not installed on this computer (https://go.dev/dl/)" >&2
		exit 1
	}
	echo "==> Checking the VM"
	local arch
	arch=$(ssh "${SSH_OPTS[@]}" "$TARGET" uname -m)
	case $arch in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*)
		echo "unsupported VM architecture: $arch" >&2
		exit 1
		;;
	esac

	echo "==> Building for linux/$arch"
	local out version
	out=$(mktemp -d)
	version=$(cd "$ROOT" && (git describe --always --dirty 2>/dev/null || date +%Y%m%d-%H%M))
	(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X main.version=$version" -o "$out/vending-server" ./cmd/vending-server)

	echo "==> Copying to $TARGET"
	scp "${SSH_OPTS[@]}" -q "$out/vending-server" "$ROOT/deploy/setup-vm.sh" "$TARGET:/tmp/"
	rm -rf "$out"
}

case $CMD in
install)
	build_and_copy
	remote "sudo $ENV_PASS bash /tmp/setup-vm.sh install $*"
	;;
update)
	build_and_copy
	remote "sudo $ENV_PASS bash /tmp/setup-vm.sh update"
	;;
reset | keys | status | logs)
	scp "${SSH_OPTS[@]}" -q "$ROOT/deploy/setup-vm.sh" "$TARGET:/tmp/"
	remote "sudo $ENV_PASS bash /tmp/setup-vm.sh $CMD"
	;;
*) usage ;;
esac
