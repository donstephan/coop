#!/usr/bin/env bash
# E2E smoke test on a throwaway tmux socket: a stub session prints a fake
# prompt and rings the bell; the coop TUI (itself driven via tmux) must
# derive NEEDS INPUT for it, live-preview it in a nested-client pane, and
# retarget the pane on selection change.
set -euo pipefail

SOCKET="coop-e2e-$$"
TMPD="$(mktemp -d)"
# coop's state under the throwaway dir: the server is started below by
# this shell, so every pane on the socket inherits it. Belt and braces
# only — nothing this run does should write state at all, and the paths
# the judge itself uses (claims, audit, judge log) deliberately ignore
# this variable, since a monitored session can set it. See hub.claimRoot.
export XDG_STATE_HOME="$TMPD/state"
cleanup() { tmux -L "$SOCKET" kill-server 2>/dev/null || true; rm -rf "$TMPD"; }
trap cleanup EXIT

# Repo + config for the create-from-picker flow. The created session
# runs `sleep 300` instead of claude.
mkdir -p "$TMPD/newproj"
printf '{"repos": ["%s"]}\n' "$TMPD/newproj" > "$TMPD/config.json"

cd "$(dirname "$0")/.."
go build -o /tmp/coop-e2e ./cmd/coop

# Stub "claude": prints a Claude-style dialog, then publishes waiting
# the way a real session's injected hooks would — piping a
# PermissionRequest payload through the real `coop hook`, which finds
# the socket in $TMUX and writes @coop_claude_status onto this pane.
# The dialog text still matters: the nested live client renders it.
# The initial sleep gives us time to enable monitor-bell BEFORE the bell
# rings (the first new-session is also what starts the throwaway server).
# Sessions get distinct start dirs: the TUI groups by session_path
# basename and no longer shows session names, so the dir basename is
# what wait_for finds in the list.
mkdir -p "$TMPD/stub" "$TMPD/zstub"
tmux -L "$SOCKET" new-session -d -s stub -c "$TMPD/stub" \
  "sleep 3; printf 'Do you want to proceed?\n❯ 1. Yes\n  2. No\n\a'; printf '{\"hook_event_name\":\"PermissionRequest\",\"session_id\":\"stub\",\"cwd\":\"$TMPD/stub\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"rm -rf build/\"}}' | /tmp/coop-e2e hook; sleep 300"
tmux -L "$SOCKET" set -g monitor-bell on

# The hub TUI in its own session on the same socket.
tmux -L "$SOCKET" new-session -d -s hub -x 100 -y 30 \
  "/tmp/coop-e2e -socket '$SOCKET' -allowed-cmds sleep,sh,bash -config '$TMPD/config.json' -claude-cmd 'sleep 300' -hooks=false -plugin=false"

wait_for() { # wait_for <pattern> <pane>
  for _ in $(seq 40); do
    tmux -L "$SOCKET" capture-pane -p -t "$2" | grep -q "$1" && return 0
    sleep 0.25
  done
  echo "FAIL: '$1' never appeared in $2" >&2
  tmux -L "$SOCKET" capture-pane -p -t "$2" >&2
  exit 1
}

# Status words appear on the live pane's title bar, never in the nav
# list — a row shows a coloured ●/○ glyph. Both come from the same
# DeriveStatuses pass, so polling the title tests the same derivation.
wait_for_title() { # wait_for_title <pattern> <pane>
  for _ in $(seq 40); do
    tmux -L "$SOCKET" display -p -t "$2" '#{pane_title}' | grep -q "$1" && return 0
    sleep 0.25
  done
  echo "FAIL: title of $2 never matched '$1'" >&2
  echo "got: $(tmux -L "$SOCKET" display -p -t "$2" '#{pane_title}')" >&2
  exit 1
}

# The TUI must create a marked live pane in the hub session.
live=""
for _ in $(seq 40); do
  live="$(tmux -L "$SOCKET" list-panes -s -t hub \
    -F '#{pane_id} #{@coop_live}' | awk '$2==1{print $1; exit}')"
  [ -n "$live" ] && break
  sleep 0.25
done
[ -n "$live" ] || { echo "FAIL: live pane never created" >&2; exit 1; }
echo "ok: live pane created ($live)"

# …and exactly one, tick after tick. Anything that loses the pane id
# (an unknown option, an unparsed marker) makes the next poll split
# another, one per second until the window runs out of width.
sleep 3
n="$(tmux -L "$SOCKET" list-panes -s -t hub -F '#{pane_id}' | wc -l)"
[ "$n" -eq 2 ] || {
  echo "FAIL: hub window has $n panes, want 2 (nav + one live pane)" >&2; exit 1; }
echo "ok: live pane not re-split on later polls"

# The nav pane is the hub window's other pane (the TUI itself). Keys and
# captures must target it explicitly: creating a session auto-focuses the
# preview, so the session's active pane doesn't stay on the nav.
nav="$(tmux -L "$SOCKET" list-panes -s -t hub -F '#{pane_id} #{@coop_live}' \
  | awk 'NF==1{print $1; exit}')"
[ -n "$nav" ] || { echo "FAIL: nav pane not found" >&2; exit 1; }

wait_for "stub" "$nav"
wait_for_title "NEEDS INPUT" "$live"
wait_for "1. Yes" "$live"      # nested client renders the stub's screen
echo "ok: discovery, status, live preview"

# The stub's hook fired above with the arbiter mode still unset (the
# default before anything in this script has touched
# @coop_arbiter_mode) — spawnJudge must bail out before it claims the
# episode, or a mode flip later would find that dialog already spent.
# The judge process itself is the observable, not the claim file: claims
# live under the password database's home (hub.claimRoot ignores
# XDG_STATE_HOME on purpose), so this run cannot look at them without
# reaching into the developer's real home. A spawned judge would be a
# live `coop-e2e judge` process — and would go on to run a real
# `claude -p`, which is exactly what must never happen here.
# The bracket is the ps|grep trick: it matches the same text, but the
# pattern string itself never appears in a command line, so a shell whose
# cmdline holds this very line cannot match itself.
judges="$(pgrep -fc "coop-e2e[ ]judge" || true)"
[ "${judges:-0}" -eq 0 ] || {
  echo "FAIL: $judges judge process(es) spawned with the arbiter mode off" >&2; exit 1; }
echo "ok: no judge spawned while arbiter mode is off"

opt_is() { # opt_is <option> <want>
  for _ in $(seq 40); do
    [ "$(tmux -L "$SOCKET" show-options -gqv "$1")" = "$2" ] && return 0
    sleep 0.25
  done
  echo "FAIL: $1 never became '$2' (got '$(tmux -L "$SOCKET" show-options -gqv "$1")')" >&2
  exit 1
}

# a toggles the socket-global mode off -> recommend -> off. It is
# pure tmux state (no hook fires from pressing it), so this is safe to
# run any time — but it runs last regardless, after every assertion
# that depends on the stub's one and only hook event, so a stray hook
# firing during the toggle is never a possibility either.
mode_is() { opt_is @coop_arbiter_mode "$1"; }
tmux -L "$SOCKET" send-keys -t "$nav" "a"
mode_is recommend
tmux -L "$SOCKET" send-keys -t "$nav" "a"
mode_is ""
echo "ok: a toggles arbiter mode off -> recommend -> off"

# A second session; selecting it must retarget the live pane.
tmux -L "$SOCKET" new-session -d -s zstub -c "$TMPD/zstub" "sleep 300"
wait_for "zstub" "$nav"
tmux -L "$SOCKET" send-keys -t "$nav" "j"    # move selection to zstub
for _ in $(seq 40); do
  tmux -L "$SOCKET" display -p -t "$live" '#{pane_start_command}' \
    | grep -q "zstub" && break
  sleep 0.25
done
tmux -L "$SOCKET" display -p -t "$live" '#{pane_start_command}' \
  | grep -q "zstub" || { echo "FAIL: live pane never retargeted" >&2; exit 1; }
echo "ok: retarget on selection change"

# Create a session from the picker: n opens it, enter creates newproj.
tmux -L "$SOCKET" send-keys -t "$nav" "n"
wait_for "pick a repo" "$nav"
tmux -L "$SOCKET" send-keys -t "$nav" Enter
for _ in $(seq 40); do
  tmux -L "$SOCKET" has-session -t "=newproj" 2>/dev/null && break
  sleep 0.25
done
tmux -L "$SOCKET" has-session -t "=newproj" 2>/dev/null \
  || { echo "FAIL: newproj session never created" >&2; exit 1; }
wait_for "newproj" "$nav"    # hub list shows it
# Auto-select lands one poll after the list shows the session — wait for
# the ▸ cursor on the row under the newproj group before arming the kill.
sel_on_newproj() {
  tmux -L "$SOCKET" capture-pane -p -t "$nav" | grep -A1 "newproj" | grep -q "▸"
}
for _ in $(seq 40); do
  sel_on_newproj && break
  sleep 0.25
done
sel_on_newproj || { echo "FAIL: newproj never auto-selected" >&2
  tmux -L "$SOCKET" capture-pane -p -t "$nav" >&2; exit 1; }
echo "ok: create from picker"

# Creating must also hand the keyboard to the preview (now showing the
# new session), so the user can start typing without pressing enter.
focused_pane() { tmux -L "$SOCKET" display -p -t hub: '#{pane_id}'; }
for _ in $(seq 40); do
  [ "$(focused_pane)" = "$live" ] && break
  sleep 0.25
done
[ "$(focused_pane)" = "$live" ] \
  || { echo "FAIL: preview not focused after create (active: $(focused_pane))" >&2; exit 1; }
echo "ok: create auto-focuses preview"

# Kill from the hub: x arms on the selected session (newproj was just
# auto-selected), y confirms; the session must vanish from the server.
tmux -L "$SOCKET" send-keys -t "$nav" "x"
wait_for "kill newproj?" "$nav"
tmux -L "$SOCKET" send-keys -t "$nav" "y"
for _ in $(seq 40); do
  tmux -L "$SOCKET" has-session -t "=newproj" 2>/dev/null || break
  sleep 0.25
done
tmux -L "$SOCKET" has-session -t "=newproj" 2>/dev/null \
  && { echo "FAIL: newproj session survived kill" >&2; exit 1; }
echo "ok: kill from hub"

# Add a repo from the picker: the last row takes a path, writes it to
# config.json and starts a session there. The write is the real one —
# unit tests stub it, so this is where the wiring is proven.
mkdir -p "$TMPD/addproj"
tmux -L "$SOCKET" send-keys -t "$nav" "n"
wait_for "add new repo" "$nav"
tmux -L "$SOCKET" send-keys -t "$nav" Down Down Enter   # past newproj to the add row
wait_for "add repo:" "$nav"
tmux -L "$SOCKET" send-keys -t "$nav" "$TMPD/addproj" Enter
for _ in $(seq 40); do
  tmux -L "$SOCKET" has-session -t "=addproj" 2>/dev/null && break
  sleep 0.25
done
tmux -L "$SOCKET" has-session -t "=addproj" 2>/dev/null \
  || { echo "FAIL: addproj session never created" >&2
       tmux -L "$SOCKET" capture-pane -p -t "$nav" >&2; exit 1; }
grep -q "$TMPD/addproj" "$TMPD/config.json" \
  || { echo "FAIL: repo not written to config: $(cat "$TMPD/config.json")" >&2; exit 1; }
echo "ok: add repo from picker"

# Quit (q arms, y confirms) must kill the live pane so the hub session
# ends with the TUI.
tmux -L "$SOCKET" send-keys -t "$nav" "q"
wait_for "quit coop?" "$nav"
tmux -L "$SOCKET" send-keys -t "$nav" "y"
for _ in $(seq 40); do
  tmux -L "$SOCKET" has-session -t hub 2>/dev/null || { echo "ok: quit ends hub session"; exit 0; }
  sleep 0.25
done
echo "FAIL: hub session survived quit (live pane not killed?)" >&2
exit 1
