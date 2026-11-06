#!/usr/bin/env bash
# Builds Dársena on macOS and runs it in experimental mode inside a dedicated
# OrbStack Linux machine with a rootless Docker daemon. It never uses the
# Docker of the Mac and never discovers a daemon socket: the first run needs
# --docker-socket, which is then kept with the installation.
#
#   scripts/orbstack.sh up [--docker-socket PATH] [--no-build] [--apps-port PORT]
#   scripts/orbstack.sh status | down | logs [-f] | credential
#
# Environment: DARSENA_ORB_MACHINE (default darsena-runtime-test),
# DARSENA_ORB_PORT (default 7320), DARSENA_ORB_HOME (default ~/darsena in the
# machine).
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
MACHINE=${DARSENA_ORB_MACHINE:-darsena-runtime-test}
PORT=${DARSENA_ORB_PORT:-7320}
APPS_PORT=7321
HOME_DIR=${DARSENA_ORB_HOME:-}
OUT=$ROOT/bin/orbstack
REMOTE_SRC=$(<"$ROOT/scripts/orbstack-remote.sh")

fail() {
	echo "error: $*" >&2
	exit 1
}
usage() {
	sed -n '7,8s/^#   //p' "${BASH_SOURCE[0]}"
	exit "${1:-0}"
}

[[ $PORT =~ ^[0-9]+$ ]] && ((PORT >= 1024 && PORT <= 65535)) || fail "DARSENA_ORB_PORT must be a port between 1024 and 65535"

remote() {
	orb -m "$MACHINE" env DARSENA_HOME="$HOME_DIR" DARSENA_PORT="$PORT" DARSENA_APPS_PORT="$APPS_PORT" bash -c "$REMOTE_SRC" orbstack-remote "$@"
}

check_machine() {
	command -v orb >/dev/null || fail "OrbStack's orb command is not installed"
	local state
	state=$(orb list 2>/dev/null | awk -v m="$MACHINE" '$1 == m {print $2}')
	[[ -n $state ]] || fail "the OrbStack machine $MACHINE does not exist; set DARSENA_ORB_MACHINE or create a dedicated machine with a rootless Docker daemon"
	[[ $state == running ]] || fail "the OrbStack machine $MACHINE is $state; start it with: orb start $MACHINE"
}

# check_mac_port fails if a Mac process already listens on the port, because
# OrbStack could not forward the machine's port to the Mac's localhost.
# Usage: check_mac_port PORT HOW_TO_CHANGE_IT
check_mac_port() {
	local who
	who=$(lsof -nP -iTCP:"$1" -sTCP:LISTEN -Fc 2>/dev/null | sed -n 's/^c//p' | sort -u | paste -sd, - || true)
	[[ -z $who ]] || fail "port $1 is busy on the Mac ($who); stop that process or choose another port with $2"
}

build() {
	command -v go >/dev/null || fail "Go is not installed on the Mac"
	command -v pnpm >/dev/null || fail "pnpm is not installed on the Mac"
	local arch goarch
	arch=$(orb -m "$MACHINE" uname -m)
	case $arch in
	aarch64 | arm64) goarch=arm64 ;;
	x86_64) goarch=amd64 ;;
	*) fail "unsupported machine architecture: $arch" ;;
	esac
	echo "building the frontend…"
	(cd "$ROOT" && pnpm build >/dev/null) || fail "the frontend build failed; run pnpm build to see the errors"
	echo "building the linux/$goarch executable…"
	rm -rf "$OUT"
	mkdir -p "$OUT"
	(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go build -trimpath -o "$OUT/darsena" ./cmd/darsena) || fail "the Go build failed"
	cp -R "$ROOT/web/dist" "$OUT/web"
}

cmd_up() {
	local socket="" skip_build=0
	while (($#)); do
		case $1 in
		--docker-socket)
			(($# >= 2)) || fail "--docker-socket needs a path"
			socket=$2
			shift 2
			;;
		--no-build)
			skip_build=1
			shift
			;;
		--apps-port)
			(($# >= 2)) || fail "--apps-port needs a port"
			APPS_PORT=$2
			shift 2
			;;
		*) usage 2 ;;
		esac
	done
	[[ $APPS_PORT =~ ^[0-9]+$ ]] && ((APPS_PORT >= 1024 && APPS_PORT <= 65535)) || fail "--apps-port must be a port between 1024 and 65535"
	((APPS_PORT != PORT)) || fail "--apps-port must differ from the server port ($PORT)"
	check_machine
	remote prepare "$socket"
	check_mac_port "$PORT" "DARSENA_ORB_PORT"
	check_mac_port "$APPS_PORT" "--apps-port"
	if ((!skip_build)); then
		build
		echo "copying to $MACHINE…"
		COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -C "$OUT" -cf - . | remote receive
		remote install
	fi
	remote provision "$socket"
	remote start
	# OrbStack forwards the machine's loopback port to the Mac shortly after it opens.
	local reachable=0
	for _ in $(seq 20); do
		if curl -fs --max-time 1 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health/live"; then
			reachable=1
			break
		fi
		sleep 0.5
	done
	if ((!reachable)); then
		echo "warning: the server runs in the machine but is not reachable from the Mac at http://127.0.0.1:$PORT; check OrbStack's port forwarding" >&2
	fi
	remote started
	echo
	echo "Sign in with the owner credential. To copy it without printing it: scripts/orbstack.sh credential"
}

cmd_credential() {
	command -v pbcopy >/dev/null || fail "pbcopy is not available"
	check_machine
	remote credential | pbcopy
	echo "owner credential copied to the clipboard"
}

case ${1:-} in
up)
	shift
	cmd_up "$@"
	;;
status | down)
	check_machine
	remote "$1"
	;;
logs)
	check_machine
	remote logs "${2:-}"
	;;
credential) cmd_credential ;;
-h | --help | help) usage ;;
*) usage 2 ;;
esac
