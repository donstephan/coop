# Per-repo toolbox containers — design

Date: 2026-08-04

Give every session coop launches a per-repo container holding the command-line
tooling that session needs, reached through transparent `PATH` shims, so a
repo's tools are declared in the repo rather than installed on the host.

## Why

Claude Code sessions need tools the host may not have: a python with the right
libraries, `mongosh`, `gh`, a service CLI, a pinned `terraform`. Today that is
the host's problem, and it fails in three ways.

- **It doesn't travel.** What a repo needs lives in a README paragraph or in
  nobody's head. A fresh clone, a second machine, or a session opened six months
  later gets a different toolchain than the one the work was done with.
- **Versions collide.** Two repos wanting different pythons, node versions or
  CLI major versions can't both be satisfied by one host, so one of them loses
  quietly.
- **Claude degrades silently when a tool is missing.** This is the one that
  matters most and is least visible. A missing tool does not produce a stop and
  a request to install it — it produces a worse path taken confidently: thirty
  lines of python where `jq` would do, a guess at collection contents where
  `mongosh` would have read them. The output looks fine. Guaranteeing the
  toolchain removes a whole class of quietly-degraded work that otherwise
  surfaces only under close review.

The prior art is `realciso-v2`: a `scripts` service in its `docker-compose.yml`
built from a five-line `Dockerfile.scripts` (python + pymongo/redis/boto3),
bind-mounting the repo at `/workspace`, running `sleep infinity`, and a
`PreToolUse` hook (`.claude/hooks/enforce-docker.sh`) that blocks bare `python`
and tells the agent to write `docker compose exec scripts …` instead. It works,
and everything specific to that repo is the package list. The mechanism —
long-lived idle container, bind mount, and something that makes the agent use it
— generalizes.

coop is well placed to host it because it already owns the launch path: it
writes `hooks-settings.json` and appends `--settings` to `claudeCmd` at startup
(`WithHookSettings`). Injecting a `PATH` prefix is the same seam.

### What this is not

Claude Code's `Read`, `Edit`, `Glob` and `Grep` tools run in-process on the
host. No hook and no shim can route them into a container, so the repo stays
host-visible no matter what this design does. **This is a Bash-tool toolchain,
not isolation.** Full isolation is only reachable by moving `claude` itself into
a container, devcontainer-style. Nothing here forecloses that, and nothing here
delivers it.

The base image alone is also not where the value is. The feature is *per-repo
declared tooling*; the generic base image is scaffolding that makes a repo's
overlay cheap to write.

### All-in or all-out, never split

The governing rule for a repo is **consistency, not capability**. Builds and
tests belong in the toolbox whenever the repo's toolchain is there at all — the
failure mode this design must avoid is not "a build ran in a container", it is
one repo's toolchain straddling two environments. `go test` in the container and
`go build` on the host means claude has to know which is which, and the shim
exists precisely to make that invisible; the result is a binary written into a
home directory nobody looks in, reported as a success.

So a repo is all-in or all-out:

- **coop** → all-in. See below; it is a better case than it first appears.
- **A python-scripts repo on a host with no python** → all-in, tests included.
  The toolbox simply is that repo's environment.
- **realciso-v2** → its own compose containers for app builds and tests, the
  toolbox only for the auxiliary python. Coherent because the boundary follows
  the runtime rather than cutting through one toolchain.

Mixing is what produces environment-coupled artifacts in the tree — a `.venv` of
debian-slim wheels, a `node_modules` built against the container's node. Those
are only hazards when something on the host also consumes them, which
consistency removes.

The base image therefore ships no compilers. A repo commits its whole toolchain
to the container deliberately, in its overlay, or not at all.

### coop's own repo is all-in

Worth stating because the first-glance read is the opposite. coop has to absorb
tmux 3.4 vs 3.5 differences (`vis(3)`-escaped `-F` output, absent
`allow-set-title`), and a host can only ever have one tmux. Pinning it in an
overlay makes the version a declared property of the repo, and eventually lets
`e2e-smoke.sh` run against both — which the host cannot do at all.
`e2e-smoke.sh` spawns a throwaway socket, so it is self-contained in a
container.

Three carve-outs remain, and they are structural rather than preference:

1. **`scripts/toolbox-smoke.sh` runs on the host.** It tests this feature and
   needs a docker CLI and socket, which are never mounted. A bootstrap
   exception; no version of this design avoids it.
2. **Running coop is a host activity.** It attaches to the host tmux server and
   watches host processes. Build and test in the container, run on the host —
   a runtime boundary, not a toolchain split.
3. **An editor is a host consumer.** `gopls` runs on the host and wants a host
   Go. The achievable claim is that *claude's* toolchain is fully
   containerized, not that the host has nothing on it.

## Shape

A new package `internal/toolbox`, alongside `hub`/`tui`/`config`. It defines an
`Engine` interface — the subset of the docker CLI coop needs — with an
`ExecEngine` that shells out and a `fakeEngine` for tests, deliberately
mirroring `Tmux`/`ExecTmux`/`fakeTmux`.

### One container per repo

Named `coop-tools-<slug>`, slug derived from the repo path. Every session on
that repo execs into the same container. Not per session: the unit the tooling
is declared for is the repo.

**There is no new state store.** Docker is the database the way tmux is —
`docker ps --filter label=coop.managed=true` *is* the query. coop persists
nothing about containers: no file, no tmux option, no refcount.

An unconfigured repo resolves to the base image and gets shims for what the base
provides. No container runs until one of them is called, so a repo that never
reaches for a shimmed tool never costs one.

### Lifecycle: started on use, reaped by itself

Two things happen at different times, and separating them is what keeps this
simple.

**At session create** (the `n` picker's `createCmd` path), coop resolves the
image — building the base if it is missing, or the overlay if it changed — and
reads its manifest to generate the shims. Best-effort and asynchronous. No
long-lived container is started; the manifest is read with a one-shot
`docker run --rm <image> cat /etc/coop/tools`, cached by image id so it runs
only when the image changes.

This ordering is forced, not chosen: shims cannot be generated from a running
container, because nothing starts the container until a shim is called.

**The container itself starts lazily**, on the first shim invocation, and the
same path is the self-heal: if `docker exec` fails because the container is
gone, `coop tools exec` starts it and retries once. One start path serves the
first use, a recovery after `docker rm -f`, and a return after an idle-out
alike. A session that predates the toolbox recovers the same way.

**Stop is the container's own job.** PID 1 is a reaper loop rather than `sleep
infinity`: every 60s, if no process other than PID 1 has run for
`idle_timeout`, exit. Run with `--rm`, so exiting removes it.

Reaping from the hub poll was considered and rejected on three counts:

- **It requires a hub.** Poll-based reaping stops the moment the TUI quits —
  precisely when the containers should go away. The arbiter already establishes
  that coop's background work must not depend on a hub being up.
- **It can't see work in flight.** "Any process besides PID 1" is a direct
  liveness signal, so a forty-minute test run keeps its own container alive by
  definition. The poll sees only panes, which is a proxy: a background process
  in a closed session would get its legs cut.
- **It is state.** Labels or refcounts to keep in sync, and two hubs on one
  socket racing over them. The container knows whether it is busy; nothing else
  has to track it.

An aggressive timeout is safe *because* the shim self-heals — an idled-out
container coming back is a ~1s hiccup on one command, with image and overlay
already built and cached.

`coop tools ls | up | stop | prune | rebuild | shell` exists as the human
surface. None of it is part of the normal path.

### Path identity

The repo bind-mounts **at its own host path**, not at `/workspace`:

```
-v /home/user/sprocket-v2:/home/user/sprocket-v2  -w "$PWD"
```

This is the deliberate departure from the realciso setup. Because the path is
identical on both sides, a traceback, a `--help` example, an absolute path in a
config file and anything claude subsequently hands to `Read` all name the same
file. There is no translation layer to get wrong, and no failure mode where
claude reads `/workspace/foo.py` out of command output and cannot open it.
Relative paths work because the shim passes the pane's `$PWD` through as the
working directory.

The mount is **read-write**. A read-only repo would make the feature feel
broken: formatters and code generators — `black`, `prettier`, `sqlfluff fix`,
`pip-compile`, `openapi-generator` — are a large fraction of why a tool is worth
shimming at all, and the workaround (write to `/tmp`, copy back) is worse than
anything it avoids. Ownership is not a concern, since `--user $(id -u):$(id -g)`
means what it writes is already the invoking user's.

Two further standard mounts:

- `/tmp` — the session scratchpad lives there, and it is already same-user
  shared space.
- A persistent per-repo home at `~/.local/state/coop/toolbox/<slug>/home`,
  exported as `HOME`. The container runs `--user $(id -u):$(id -g)`, which
  leaves no passwd entry inside; pip, npm and gh all need a writable home.

**The container inherits almost no environment.** `TERM`, `LANG`, `LC_*` and
`TZ` pass through so text renders correctly; nothing else does. The image's own
`ENV` is authoritative, which means repo-specific environment belongs in the
overlay, where it is declared once and travels with the repo rather than
depending on whatever the operator's shell happened to export. The cost is that
a variable a tool needs must be named somewhere — and naming it in the overlay
is the behaviour this feature exists to produce.

**Caches point at that home**, not the repo: the container exports `GOCACHE`,
`PIP_CACHE_DIR`, `npm_config_cache` and `CARGO_HOME` into it. This is
load-bearing rather than hygiene once a repo is all-in — without it every
idle-out means a cold rebuild, and the default location for environment-coupled
state would be the repo tree.

**The sharp edge: `$HOME` is not your home.** Anything installing into it —
`go install`, `pip install --user`, `npm i -g`, `cargo install` — succeeds,
works for the rest of that command, and is invisible from the host forever. It
survives an idle-out, because the home persists, but nothing on the host will
ever find it. The only installs that reach the host are those targeting a
mounted host path.

### Repo-declared mounts

`~/.local/bin` is the common case: `go build -o ~/.local/bin/coop` otherwise
writes into the container's home and vanishes. It is deliberately **not** a
standard mount. The repo mount's blast radius is one repo; `~/.local/bin` is on
the host `PATH` in every shell, so a stray `pip install --user` in an unrelated
repo's container silently placing executables there is a bad accident. Nor is it
the only case of its shape — a repo whose overlay carries `aws` wants `~/.aws`,
one with `gh` wants `~/.config/gh`.

So a repo opts in, in `.coop/toolbox.json` (below), and two constraints follow
from decisions already made:

- **No `src:dst` remapping.** A mount names a host path and appears at that same
  path. Path identity is the invariant the design rests on; remapping would
  reintroduce the `/workspace` translation problem somewhere new.
- **Read-only unless `:rw` is explicit.** `~/.local/bin` needs write; `~/.aws`
  emphatically does not.

`~` expands against the password database rather than `$HOME`, matching what
`JudgeEnv` already does for the judge path.

**coop refuses to mount `~/.config/coop` or `~/.local/state/coop`.** Not as a
boundary — a session with code execution writes those directly on the host, so
this stops nothing determined. It is the reasoning behind the arbiter's gates:
it prevents an unattended own-goal where a container rewrites `arbiter.md` or
the claims directory by accident, and it costs one check.

### Shims

Two-line stubs, one per manifest entry:

```sh
#!/bin/sh
exec coop tools exec <slug> python "$@"
```

All the intelligence is in `coop tools exec`, in Go and unit-testable: resolve
the container, start it if absent, then `syscall.Exec` the docker binary
directly. Exec'ing rather than wrapping passes exit codes and signals through
exactly with no lingering parent, so `^C` on a test run does the right thing.
`-i` for stdin; never `-t` — the Bash tool has no tty and `-t` would fail.

Shims are generated at session create into
`~/.local/state/coop/toolbox/<slug>/bin/`, from the image's own manifest
(below), and regenerated when the manifest hash changes. coop prepends that
directory to `PATH` in the session's `claudeCmd`, at the `WithHookSettings`
seam.

Because generation is asynchronous, the `PATH` prefix is injected whenever the
toolbox is enabled and the engine is present, before the shim directory
necessarily has anything in it. The path is deterministic from the slug, and a
`PATH` entry naming a briefly empty directory is harmless: a command run in that
window finds the host's tool, as it would today.

**Precedence.** The shim directory goes first, so the container's `python` beats
the host's — that is the point of the feature. An absolute path
(`/usr/bin/python`) still bypasses it, which is escape hatch enough.

**Failure.** Fail open at generation, fail loud at exec. If docker is absent or
the image will not resolve, no shim directory is generated, `PATH` is untouched
and coop behaves exactly as it does today. A container that cannot start
produces one line naming `coop tools rebuild`, not a mystery.

### The image declares its own tools

The image carries a manifest at `/etc/coop/tools`, one command per line. coop
shims exactly those. A repo adding a tool appends one line; there is no list in
coop's config to drift out of sync with the image, and no per-repo config needed
for the common case.

**What belongs in a manifest.** Each shim costs ~30–80ms of `docker exec`
startup. That is noise for `python`, `mongosh`, `terraform`, `aws`. It is
ruinous for `grep`/`ls`/`cat` in a loop — and claude reaches for its own `Grep`
and `Glob` tools for that work anyway, which run on the host regardless. So the
default manifest carries language runtimes, package managers and service CLIs,
and deliberately not core file utilities. Because the image declares the list,
this is a rule about how the image is authored, not code that enforces it.

### Base image and repo overlay

The base Dockerfile is embedded in the coop binary with `go:embed` and built on
first use — no registry, no second repo to keep in sync. It is tagged
`coop-tools:base-<hash-of-dockerfile>` plus a moving `coop-tools:base` tag, so
overlays have a stable name to write. A coop upgrade changes the hash and
rebuilds automatically; the superseded image survives until `coop tools prune`.

It is **debian-slim, not alpine**: musl breaks enough pip wheels that alpine
would manufacture exactly the "why won't this install" problem the feature
exists to remove. Contents are generic — python3 + pip, jq, curl, git, gh, make
— and nothing repo-specific.

A repo overlays it with an optional `.coop/tools.Dockerfile`:

```dockerfile
FROM coop-tools:base
RUN pip install --no-cache-dir pymongo redis boto3 tabulate
RUN echo mongosh >> /etc/coop/tools
```

Built as `coop-tools-<slug>:<hash of overlay + base>`, so editing the overlay
rebuilds and so does a coop upgrade. That one `echo` is the entire contract for
adding a shim. With no overlay, the base image is used directly.

coop's own overlay is the all-in example:

```dockerfile
FROM coop-tools:base
COPY --from=golang:1.23-bookworm /usr/local/go /usr/local/go
RUN apt-get update && apt-get install -y --no-install-recommends tmux \
    && rm -rf /var/lib/apt/lists/*
ENV PATH=/usr/local/go/bin:$PATH CGO_ENABLED=0
RUN printf 'go\ngofmt\ntmux\n' >> /etc/coop/tools
```

`CGO_ENABLED=0` is the load-bearing line. It makes the binary fully static and
switches Go to its pure-Go user lookup, reading `/etc/passwd` directly — so a
binary built on debian-slim runs on the host with no libc coupling. Without it
`os/user` links glibc NSS and correctness depends on version skew being
forgiving. Any repo building a host-runnable artifact needs the equivalent.

Builds run asynchronously at session create; failures land in
`~/.local/state/coop/toolbox/build.log` and leave no shim directory (fail open).
`coop tools rebuild` runs the build in the foreground with output on the
terminal, for a human diagnosing one.

### What a Dockerfile cannot express: `.coop/toolbox.json`

Two things a repo needs that no `FROM` line can state — the network the
container joins, and the host paths it can see:

```json
{
  "network": "sprocket-v2_default",
  "mounts": ["~/.local/bin:rw", "~/.aws:ro"]
}
```

`network` is what a repo with its own compose stack needs — realciso's scripts
container reaches mongo and redis by sitting on the compose network. Explicit,
never inferred from a nearby `docker-compose.yml`: guessing a network name is
the kind of magic that is charming until it attaches to the wrong one.

`mounts` is described under *Repo-declared mounts* above. coop's own file is one
line:

```json
{ "mounts": ["~/.local/bin:rw"] }
```

Two optional repo-owned files in total — `tools.Dockerfile` and `toolbox.json`
— both absent by default.

### coop config

A `toolbox` block in `config.json`: `enabled` (default true — an absent engine
already disables it in practice), `engine` (`docker`|`podman`), `idle_timeout`
(default `30m`, `0` = never). Written through the same key-by-key
rewrite `AddRepo` already uses, so settings coop does not model survive.

## Security posture

**This is not a security boundary**, for the same reasons the arbiter is not.
The repo is bind-mounted read-write at its host path, the container runs as the
invoking user's uid, and `/tmp` is shared — anything with code execution inside
can rewrite the repo and the user's scratch space, and a container joined to a
compose network reaches whatever that network reaches.

**The docker socket is never mounted.** That single line would turn any shimmed
tool into host root, and it is the one mistake in this design space that is not
recoverable. It is also why `toolbox-smoke.sh` stays on the host.

A repo-declared `mount` widens what one container reaches, deliberately and
visibly, in a file that travels with the repo. `~/.config/coop` and
`~/.local/state/coop` are refused — not because refusing them stops anything
determined, but because it stops an accident.

What the toolbox buys is a reproducible toolchain and a clean host. The
documentation should say so in those words rather than let the word "container"
imply a sandbox.

## Testing

Following the existing split: unit tests never touch docker.

- `internal/toolbox` tests use `fakeEngine`, covering shim rendering, manifest
  parsing, image and overlay hashing, exec-argument construction, and the reaper
  script. Anything written goes under `t.TempDir()`; fixture paths are generic
  (`/home/user/sprocket-v2`), never the developer's own.
- `scripts/toolbox-smoke.sh` covers real docker: build, start, path identity
  from a subdirectory, a repo-declared mount resolving to the same host path,
  self-heal after `docker rm -f`, and idle-out with a 5s timeout override. It
  skips with a message when docker is absent, the way `e2e-smoke.sh` requires
  tmux ≥ 3.2. It runs on the host, always — see *Security posture*.

Once coop's own overlay exists, `go test ./...` and `scripts/e2e-smoke.sh` run
inside it, against a pinned tmux. That is the point of coop being all-in, and it
is also the dogfooding: if the toolbox is not good enough to build coop with, it
is not good enough.

`CLAUDE.md` gains a section describing the toolbox at the density of the
existing ones.

## Accepted gaps and v2

- **Repos with an existing compose stack end up with two container stories** —
  `docker compose exec app` for the repo's own services, the toolbox for its
  scripts. The toolbox does not replace a repo's containers. The clean
  resolution is a later `.coop/toolbox.json` that names an *existing* compose
  service to exec into instead of building an image, collapsing the two back
  into one. It is deliberately not in v1: the build path is what serves repos
  with no container story at all, and those are the ones with nothing today.
- **Sessions started outside coop get no shims.** `PATH` is injected at launch,
  so a session coop did not start is unaffected — the same shape as the hook
  status tier, where a pane that publishes nothing falls back to what the host
  provides.
- **Isolation is out of scope.** Read/Edit/Glob/Grep run on the host; see *What
  this is not*.
- **No image garbage collection beyond `coop tools prune`.** Superseded base
  and overlay images accumulate across upgrades until pruned by hand.
- **A repo can still be split, and coop cannot detect it.** The all-in rule is a
  convention, not something the design enforces: an overlay carrying `go` while
  the host also has one is indistinguishable from the intended arrangement. The
  observable symptom is an artifact appearing somewhere unexpected, and the
  guidance is the mitigation.
- **`$HOME` installs are invisible from the host** unless they target a declared
  mount. Documented as a sharp edge rather than solved; solving it would mean
  mounting the real home, which reintroduces every collision the toolbox
  exists to prevent.
