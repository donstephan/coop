#!/usr/bin/env bash
# End-to-end check of the per-repo toolbox against a real container
# engine. Unit tests never touch docker (internal/toolbox uses fakeEngine
# throughout); this is the other half.
#
# Runs on the host, always: it needs a docker CLI and socket, and the
# toolbox never mounts one.
set -euo pipefail

command -v docker >/dev/null 2>&1 || {
	echo "SKIP: docker not installed"
	exit 0
}
docker info >/dev/null 2>&1 || {
	echo "SKIP: docker installed but not usable by this user"
	exit 0
}

root=$(cd "$(dirname "$0")/.." && pwd)
# Deliberately NOT under /tmp: every container gets /tmp bind-mounted
# unconditionally, so a repo and a declared-mount target under it would
# be visible inside whatever this test declares — and the path-identity,
# cwd-passthrough, declared-mount and ownership checks below would all
# pass with the declared-mount code path deleted.
work=$(mktemp -d "${HOME:?}/coop-toolbox-smoke.XXXXXX")
coop="$work/coop"
repo="$work/sprocket-v2"
# The shim directory coop writes for this repo, derived the same way
# toolbox.Slug does it: basename plus the first four bytes of the
# sha256 of the cleaned absolute path.
slug="sprocket-v2-$(printf '%s' "$repo" | sha256sum | cut -c1-8)"
shimdir="$HOME/.local/state/coop/toolbox/$slug/bin"
# coop tools reads config.DefaultPath(), which derives from the password
# database — XDG_CONFIG_HOME cannot redirect it, by design (that lookup
# must not be something a monitored session can steer). So this test runs
# against the operator's real config.json and only ever touches the
# container, images and state for a repo under $work, which no real
# session can own; it never touches another repo's.
cleanup() {
	[ -x "$coop" ] && "$coop" tools stop "$repo" >/dev/null 2>&1
	docker images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null |
		grep "^coop-tools-$slug:" | xargs -r docker rmi -f >/dev/null 2>&1
	rm -rf "$work" "$HOME/.local/state/coop/toolbox/$slug"
	return 0
}
trap cleanup EXIT

echo "== build coop"
go build -o "$coop" "$root/cmd/coop"

mkdir -p "$repo/scripts" "$repo/.coop" "$work/outbin"
echo 'print("hello from", __file__)' >"$repo/scripts/hi.py"
cat >"$repo/.coop/toolbox.json" <<EOF
{ "mounts": ["$work/outbin:rw"] }
EOF

echo "== up (first run: builds the base image, several minutes)"
"$coop" tools up "$repo"

cname=$("$coop" tools ls | awk '/sprocket-v2/ {print $1; exit}')
[ -n "$cname" ] || {
	echo "FAIL: container not found in 'coop tools ls'"
	"$coop" tools ls
	exit 1
}
echo "ok: container up ($cname)"

echo "== path identity: an absolute path resolves to the same file as on the host"
out=$("$coop" tools exec "$repo" python3 "$repo/scripts/hi.py")
case "$out" in
*"$repo/scripts/hi.py"*) ;;
*)
	echo "FAIL: path identity: $out"
	exit 1
	;;
esac
echo "ok: absolute path identity"

echo "== path identity: a relative path from a subdirectory works because cwd is passed through"
out=$(cd "$repo/scripts" && "$coop" tools exec "$repo" python3 hi.py)
case "$out" in
*hello*) ;;
*)
	echo "FAIL: relative path from a subdirectory: $out"
	exit 1
	;;
esac
echo "ok: cwd passed through"

echo "== a repo-declared mount reaches the host at the same path"
"$coop" tools exec "$repo" python3 -c "open('$work/outbin/marker', 'w').write('ok')"
[ -f "$work/outbin/marker" ] || {
	echo "FAIL: declared mount did not reach the host at its own path"
	exit 1
}
echo "ok: declared mount is the same path on both sides"

echo "== files the container writes are owned by the invoking user, not root"
owner=$(stat -c %u "$work/outbin/marker")
[ "$owner" = "$(id -u)" ] || {
	echo "FAIL: marker owned by uid $owner, want $(id -u)"
	exit 1
}
echo "ok: container writes as the invoking user"

echo "== the docker socket is not visible inside the container"
if "$coop" tools exec "$repo" test -e /var/run/docker.sock 2>/dev/null; then
	echo "FAIL: docker socket is visible inside the container"
	exit 1
fi
echo "ok: docker socket absent"

echo "== self-heal: docker rm -f, then the next command transparently restarts it"
docker rm -f "$cname" >/dev/null
out=$("$coop" tools exec "$repo" python3 -c "print('back')")
case "$out" in
*back*) ;;
*)
	echo "FAIL: did not self-heal after docker rm -f: $out"
	exit 1
	;;
esac
# The restart must reuse the same name (one container per repo) rather
# than drift onto a different one.
cname2=$("$coop" tools ls | awk '/sprocket-v2/ {print $1; exit}')
[ "$cname2" = "$cname" ] || {
	echo "FAIL: self-heal produced a different container name: $cname2 vs $cname"
	exit 1
}
echo "ok: self-heal restarted the same container"

echo "== a command's exit code passes through"
code=0
"$coop" tools exec "$repo" python3 -c "import sys; sys.exit(7)" || code=$?
[ "$code" = 7 ] || {
	echo "FAIL: exit code $code, want 7"
	exit 1
}
echo "ok: exit code passed through"

echo "== a running container is reconciled against a changed image"
# The container is up on the base image right now. Adding an overlay that
# carries a new command must not leave the old container serving it —
# that is "executable file not found" for a tool the repo just declared.
cat >"$repo/.coop/tools.Dockerfile" <<'EOF'
FROM coop-tools:base
RUN printf '#!/bin/sh\necho overlay-tool-ok\n' > /usr/local/bin/sprocket \
    && chmod +x /usr/local/bin/sprocket
RUN printf '%s\n' sprocket docker tmux >> /etc/coop/tools
EOF
out=$("$coop" tools exec "$repo" sprocket)
case "$out" in
*overlay-tool-ok*) ;;
*)
	echo "FAIL: container not reconciled against the new image: $out"
	exit 1
	;;
esac
echo "ok: stale container recreated on the resolved image"

echo "== shims cover the manifest and never the reserved names"
"$coop" tools rebuild "$repo" >/dev/null
for tool in sprocket python3 jq curl; do
	[ -x "$shimdir/$tool" ] || {
		echo "FAIL: no shim for $tool in $shimdir"
		ls -l "$shimdir" || true
		exit 1
	}
done
# tmux and docker are in this repo's manifest above; a shim for either
# would reroute coop's own plumbing through the container. gh and make
# are installed in the base image and deliberately unshimmed.
for tool in tmux docker coop podman gh make; do
	if [ -e "$shimdir/$tool" ]; then
		echo "FAIL: $tool must never be shimmed"
		exit 1
	fi
done
echo "ok: manifest shimmed, reserved names dropped"

echo "== the reaper is PID 1"
# Checked from inside the container's own PID namespace (docker exec
# joins it by default), not from docker inspect's Config.Cmd — that only
# describes what was configured to run, not what is actually holding
# PID 1 once the container is up. /proc/1/cmdline is NUL-separated;
# translate to spaces so a case match can see the whole argv, including
# the reaper script's own embedded newlines.
comm=$("$coop" tools exec "$repo" cat /proc/1/comm)
[ "$comm" = "bash" ] || {
	echo "FAIL: PID 1 is not bash (comm=$comm)"
	exit 1
}
cmdline=$("$coop" tools exec "$repo" sh -c "tr '\\0' ' ' < /proc/1/cmdline")
case "$cmdline" in
*"coop-activity"*) ;;
*)
	echo "FAIL: PID 1 is not the reaper: $cmdline"
	exit 1
	;;
esac
echo "ok: PID 1 is the reaper"

echo "== stop"
"$coop" tools stop "$repo"
docker inspect "$cname" >/dev/null 2>&1 && {
	echo "FAIL: container survived stop"
	exit 1
}
echo "ok: stop removes the container"

echo "PASS"
