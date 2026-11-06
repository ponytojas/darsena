#!/usr/bin/env bash
# Runs inside the dedicated OrbStack Linux machine. scripts/orbstack.sh passes it
# to bash -c, keeping stdin free for uploads; it is not meant to be run directly.
#
# It manages only its own systemd user units (darsena-worker, darsena-server)
# and files below $DARSENA_HOME. It never stops other processes, never deletes
# persisted data and never prints credential or identity contents.
set -euo pipefail
umask 077

BASE=${DARSENA_HOME:-$HOME/darsena}
PORT=${DARSENA_PORT:-7320}
APPS_PORT=${DARSENA_APPS_PORT:-7321}
APP=$BASE/app
DATA=$BASE/data
RUN=$BASE/run
LOG=$BASE/log
SOCK=$RUN/worker.sock
WORKER=darsena-worker
SERVER=darsena-server
ORIGIN=http://127.0.0.1:$PORT
LOG_LIMIT=$((10 * 1024 * 1024))

fail() {
	echo "error: $*" >&2
	exit 1
}
active() { systemctl --user is-active --quiet "$1.service"; }

# private_dir creates a 0700 directory or checks that an existing one is a
# real directory owned by this account and not accessible by others.
private_dir() {
	if [[ -L $1 ]]; then
		fail "$1 is a symlink; use a real private directory"
	elif [[ -d $1 ]]; then
		read -r owner mode < <(stat -c '%u %a' "$1")
		[[ $owner == "$(id -u)" && $mode == 700 ]] || fail "$1 must be owned by $(id -un) with mode 0700 (it is $mode); fix it with: chmod 700 $1"
	elif [[ -e $1 ]]; then
		fail "$1 exists and is not a directory"
	else
		mkdir -m 700 "$1"
	fi
}

check_tools() {
	local missing=()
	for tool in systemd-run systemctl curl ss stat python3 tar; do
		command -v "$tool" >/dev/null || missing+=("$tool")
	done
	((${#missing[@]} == 0)) || fail "missing tools in the machine: ${missing[*]}"
	[[ $BASE == /* ]] || fail "DARSENA_HOME must be an absolute path"
}

# listening reports whether a process listens on the given Unix socket path.
listening() { ss -xlnH | awk '{print $5}' | grep -qxF "$1"; }

check_machine_port() {
	if [[ -n $(ss -tlnH "sport = :$PORT") ]]; then
		fail "port $PORT is busy in the machine; choose another with DARSENA_ORB_PORT or check it with: ss -tlnp 'sport = :$PORT'"
	fi
	if [[ -n $(ss -tlnH "sport = :$APPS_PORT") ]]; then
		fail "port $APPS_PORT is busy in the machine; choose another with --apps-port or check it with: ss -tlnp 'sport = :$APPS_PORT'"
	fi
}

cmd_prepare() {
	check_tools
	if active "$WORKER" || active "$SERVER"; then
		fail "Dársena is running; stop it first with: scripts/orbstack.sh down"
	fi
	private_dir "$BASE"
	private_dir "$DATA"
	private_dir "$RUN"
	private_dir "$LOG"
	socket_setting "${1:-}"
	check_machine_port
	rm -rf "$BASE/app.new"
	mkdir -m 700 "$BASE/app.new"
}

cmd_receive() { tar -xf - -C "$BASE/app.new"; }

cmd_install() {
	[[ -x $BASE/app.new/darsena && -f $BASE/app.new/web/index.html ]] || fail "incomplete upload; run up again"
	rm -rf "$BASE/app.previous"
	if [[ -d $APP ]]; then mv "$APP" "$BASE/app.previous"; fi
	mv "$BASE/app.new" "$APP"
}

# socket_setting checks the requested daemon socket against the one kept with
# the installation; with "write" it stores the first one. It never guesses.
socket_setting() {
	local requested=$1 write=${2:-}
	local configured=""
	[[ -f $DATA/docker-socket ]] && configured=$(<"$DATA/docker-socket")
	if [[ -n $requested ]]; then
		[[ $requested == /* ]] || fail "--docker-socket must be an absolute path"
		[[ -S $requested && ! -L $requested ]] || fail "$requested is not a Unix socket; check the rootless daemon with: systemctl --user status docker"
		if [[ -n $configured && $configured != "$requested" ]]; then
			fail "this installation already uses the daemon socket $configured. Changing the daemon would leave its containers unmanaged; to change it on purpose, delete $DATA/docker-socket first"
		fi
		if [[ -z $configured && $write == write ]]; then
			printf '%s\n' "$requested" >"$DATA/docker-socket"
			echo "created: daemon socket setting ($requested)"
		fi
		configured=$requested
	fi
	if [[ -z $configured ]]; then
		local hint=""
		[[ -n ${XDG_RUNTIME_DIR:-} && -S $XDG_RUNTIME_DIR/docker.sock ]] && hint=" The rootless daemon of this account listens at $XDG_RUNTIME_DIR/docker.sock; check it is the dedicated one before using it."
		fail "the first run needs the dedicated rootless Docker socket: scripts/orbstack.sh up --docker-socket <path>.$hint"
	fi
}

# provision creates only missing initial files. Existing files are never
# overwritten, and a file whose loss would orphan data is never regenerated.
cmd_provision() {
	local requested=${1:-}
	check_tools
	[[ -x $APP/darsena ]] || fail "no build installed in the machine; run: scripts/orbstack.sh up"
	private_dir "$BASE"
	private_dir "$DATA"
	private_dir "$RUN"
	private_dir "$LOG"

	socket_setting "$requested" write

	if [[ ! -f $DATA/installation ]]; then
		for db in state.db config.db intent.db; do
			[[ -e $DATA/$db ]] && fail "$DATA/installation is missing but $db exists. A new installation identity would leave existing services unmanaged; restore the file from your backup"
		done
		printf 'darsena-%s\n' "$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')" >"$DATA/installation"
		echo "created: installation identity"
	fi
	grep -qxE '[a-z0-9][a-z0-9-]{0,62}' "$DATA/installation" || fail "$DATA/installation is not a valid installation identity; restore it from your backup"

	if [[ ! -f $DATA/identity.age ]]; then
		[[ -e $DATA/config.db ]] && fail "$DATA/identity.age is missing but config.db exists. A new identity cannot decrypt the saved configuration; restore the identity from your backup"
		"$APP/darsena" age-init --age-identity "$DATA/identity.age" 2>/dev/null || fail "could not create the age identity"
		echo "created: age identity (keep a recovery copy apart from the databases)"
	fi
	if [[ ! -f $DATA/owner.credential ]]; then
		"$APP/darsena" owner-init --owner-credential "$DATA/owner.credential" 2>/dev/null || fail "could not create the owner credential"
		echo "created: owner credential"
	fi
}

rotate_log() {
	if [[ -f $1 ]] && (($(stat -c %s "$1") > LOG_LIMIT)); then mv -f "$1" "$1.1"; fi
}

show_log() {
	echo "--- last lines of $1 ---" >&2
	tail -n 15 "$1" >&2 2>/dev/null || true
}

cmd_start() {
	check_tools
	if active "$WORKER" || active "$SERVER"; then
		fail "Dársena is already running; use status, or down and then up"
	fi
	local docker_socket installation
	docker_socket=$(<"$DATA/docker-socket")
	installation=$(<"$DATA/installation")
	[[ -S $docker_socket ]] || fail "the Docker socket $docker_socket does not exist; check the rootless daemon with: systemctl --user status docker"
	if [[ -e $SOCK || -L $SOCK ]]; then
		if listening "$SOCK"; then
			fail "another process is listening on $SOCK; it is not managed by this script, check it with: ss -xlp | grep worker.sock"
		fi
		[[ -S $SOCK && ! -L $SOCK ]] || fail "$SOCK exists and is not a socket; inspect it before removing it"
		rm -f "$SOCK"
		echo "removed a stale worker socket left by a previous run"
	fi
	check_machine_port

	rotate_log "$LOG/worker.log"
	rotate_log "$LOG/server.log"
	systemd-run --user --quiet --collect --unit="$WORKER" \
		--property=UMask=0077 --property=TimeoutStopSec=20 \
		--property=StandardOutput="append:$LOG/worker.log" --property=StandardError="append:$LOG/worker.log" \
		"$APP/darsena" worker --experimental --installation "$installation" \
		--state "$DATA/state.db" --socket "$SOCK" --config-db "$DATA/config.db" \
		--age-identity "$DATA/identity.age" --intent-db "$DATA/intent.db" --docker-socket "$docker_socket"
	for _ in $(seq 50); do
		[[ -S $SOCK ]] && break
		active "$WORKER" || break
		sleep 0.2
	done
	if [[ ! -S $SOCK ]]; then
		systemctl --user stop "$WORKER.service" 2>/dev/null || true
		show_log "$LOG/worker.log"
		fail "the worker did not start"
	fi

	systemd-run --user --quiet --collect --unit="$SERVER" \
		--property=UMask=0077 --property=TimeoutStopSec=10 \
		--property=StandardOutput="append:$LOG/server.log" --property=StandardError="append:$LOG/server.log" \
		"$APP/darsena" serve --experimental --installation "$installation" \
		--addr "127.0.0.1:$PORT" --apps-addr "127.0.0.1:$APPS_PORT" --origin "$ORIGIN" --web "$APP/web" \
		--worker-socket "$SOCK" --owner-credential "$DATA/owner.credential"
	for _ in $(seq 50); do
		if curl -fs --max-time 1 -o /dev/null "$ORIGIN/api/v1/health/live"; then
			printf '%s\n' "$ORIGIN" >"$RUN/origin"
			printf '%s\n' "$APPS_PORT" >"$RUN/apps-port"
			return 0
		fi
		active "$SERVER" || break
		sleep 0.2
	done
	systemctl --user stop "$SERVER.service" "$WORKER.service" 2>/dev/null || true
	show_log "$LOG/server.log"
	fail "the server did not start; both processes were stopped"
}

summary() {
	curl -fs --max-time 5 "$ORIGIN/api/v1/capabilities" | python3 -c '
import json, sys
c = json.load(sys.stdin)
r = c.get("real_execution", {})
print("mode:      ", c.get("mode"), "(real execution enabled)" if r.get("enabled") else "")
print("provider:  ", r.get("provider"))
print("worker:    ", c.get("worker", {}).get("status"))
print("state/vault:", c.get("state"), "/", c.get("vault"))
' || echo "capabilities: not available"
}

images() {
	local sock
	sock=$(<"$DATA/docker-socket")
	command -v docker >/dev/null || return 0
	echo "local images pinned by digest (paste one into a new definition):"
	docker --host "unix://$sock" image ls --digests --format '{{.Repository}}@{{.Digest}}' 2>/dev/null | grep -v '@<none>$' | sed 's/^/  /' || echo "  none"
	echo "  note: pause only exercises the lifecycle; it does not serve HTTP"
}

cmd_status() {
	local w s
	w=$(systemctl --user is-active "$WORKER.service" 2>/dev/null || true)
	s=$(systemctl --user is-active "$SERVER.service" 2>/dev/null || true)
	echo "worker:     ${w:-inactive}"
	echo "server:     ${s:-inactive}"
	if [[ $s == active ]]; then
		[[ -f $RUN/origin ]] && ORIGIN=$(<"$RUN/origin")
		echo "url:        $ORIGIN"
		[[ -f $RUN/apps-port ]] && APPS_PORT=$(<"$RUN/apps-port")
		echo "apps:       http://<domain>.localhost:$APPS_PORT"
		summary
	fi
	[[ -f $DATA/owner.credential ]] && echo "credential: $DATA/owner.credential"
	if [[ $w != active && ( -S $SOCK ) ]] && ! listening "$SOCK"; then
		echo "note:       stale worker socket; the next up removes it"
	fi
	return 0
}

cmd_started() {
	echo
	echo "Dársena is running in experimental mode"
	echo "url:        $ORIGIN"
	echo "apps:       http://<domain>.localhost:$APPS_PORT"
	summary
	echo "credential: $DATA/owner.credential (in the machine)"
	echo "data:       $DATA"
	echo "logs:       $LOG"
	images
}

cmd_down() {
	local stopped=0
	for unit in "$SERVER" "$WORKER"; do
		if active "$unit"; then
			systemctl --user stop "$unit.service"
			echo "stopped: $unit"
			stopped=1
		fi
	done
	((stopped)) || echo "Dársena was not running"
	if [[ -S $SOCK ]] && ! listening "$SOCK"; then rm -f "$SOCK"; fi
	echo "data kept in $DATA"
}

cmd_logs() {
	local follow=${1:-}
	[[ -d $LOG ]] || fail "no logs yet"
	if [[ $follow == -f ]]; then
		exec tail -n 20 -F "$LOG/worker.log" "$LOG/server.log"
	fi
	tail -n 40 "$LOG/worker.log" "$LOG/server.log" 2>/dev/null || true
}

cmd_credential() {
	[[ -f $DATA/owner.credential ]] || fail "no owner credential yet; run up first"
	tr -d '\n' <"$DATA/owner.credential"
}

case ${1:-} in
receive | install | start | status | down | started | credential) "cmd_$1" ;;
prepare) cmd_prepare "${2:-}" ;;
provision) cmd_provision "${2:-}" ;;
logs) cmd_logs "${2:-}" ;;
*) fail "unknown remote command" ;;
esac
