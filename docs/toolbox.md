# The toolbox — setup and use

The toolbox gives each repo a container holding the command-line tooling that
repo needs. Sessions coop launches get transparent shims on `PATH`, so `python3`
or `mongosh` in a Claude Code session runs inside the container without anyone —
you or claude — having to say so.

The point is that a repo *declares* its tooling, in the repo, instead of you
installing it on the host and hoping the next machine matches.

## Requirements

Docker installed and usable by your user (`docker info` succeeds). That's it.
Podman works too — set `toolbox.engine` in `config.json`.

If docker isn't there, nothing breaks: no shims get generated, `PATH` is
untouched, and coop behaves exactly as it does today.

## The zero-config case

Nothing to set up. Start a session from coop's `n` picker and the repo gets the
base image, which shims `python3`, `pip3`, `jq` and `curl`. Those four are on
the session's `PATH` ahead of any host copies.

`git`, `gh` and `make` are *installed* in the image but not shimmed, on purpose.
The container has no `~/.config/gh` and no `GH_TOKEN`, so a shimmed `gh` would
turn a `gh pr list` that worked yesterday into "please run: gh auth login"; and
`make` drives a whole build while the base image ships no compilers, which is
the split toolchain the all-in/all-out rule below exists to prevent. A repo that
wants either can shim it in its overlay, where it can also supply what it needs.

The container starts on the first command that uses one of them, and exits
itself after 30 minutes with nothing running. You will never type a command to
start or stop one.

The very first session on a machine has to build the base image, which takes a
few minutes. That happens in the background while the session is already
running, so commands in the first minute or two may still find the host's tools
— the shims appear when the build finishes. If it fails, the reason is in
`~/.local/state/coop/toolbox/build.log`, no shims are written, and the session
carries on with whatever the host has.

Sessions you start outside coop get no shims: the `PATH` prefix is added when
coop launches a session, so a `claude` you started yourself is unaffected.

## Where `coop-tools:base` comes from

**It is built on your machine, not pulled.** Nothing named `coop-tools` exists
on Docker Hub or any other registry.

The Dockerfile is embedded in the coop binary and built the first time a session
needs it, tagged `coop-tools:base-<hash of that Dockerfile>`. The moving
`coop-tools:base` tag that overlays write in their `FROM` line is a pointer to
whichever hash is current.

What *is* pulled from Docker Hub is `debian:bookworm-slim`, the base of the
base — so the first build needs network, and the trust chain is the ordinary one
for any Dockerfile: Debian's official image, Debian's apt repos, and PyPI if an
overlay pip-installs. coop adds nothing to that chain and hosts nothing.

Building rather than publishing means there's no registry to depend on and no
version skew: the tag comes from the Dockerfile's hash, so upgrading coop
changes the hash and the next session rebuilds by itself. The superseded image
stays until `coop tools prune`.

Cost is a few minutes, once per coop version. Every repo on the machine reuses
the result, and overlays are cached on top of it.

One thing the image does that a bookworm veteran will not expect: **`pip
install` just works.** The base image deletes Debian's PEP 668
`EXTERNALLY-MANAGED` marker, because the container *is* the environment — there
is no host Python for that marker to protect, and leaving it in place would fail
every `pip install` in an overlay or a session with
`error: externally-managed-environment`.

## Adding tools for a repo

Create `.coop/tools.Dockerfile` in the repo:

```dockerfile
FROM coop-tools:base

RUN pip install --no-cache-dir pymongo redis boto3 tabulate
RUN apt-get update && apt-get install -y --no-install-recommends \
        postgresql-client \
    && rm -rf /var/lib/apt/lists/*

# One line per command you want shimmed onto the session's PATH.
RUN printf '%s\n' psql >> /etc/coop/tools
```

Two things to know:

- **`FROM coop-tools:base` is always right.** coop keeps that tag pointing at
  the current base image, so you never write a hash.
- **The manifest is what gets shimmed**, not what's installed. `pip install
  pymongo` makes it importable from the already-shimmed `python3`; you only
  append to `/etc/coop/tools` for a new *command*.
- **The build context is `.coop/`, not the repo.** `COPY` can only reach files
  next to the Dockerfile — an image that depended on your working tree would
  rebuild on every edit and mean something different on every checkout.
  Anything else you need, install in the image or reach for at run time; the
  repo is mounted when the container runs.

Then rebuild:

```
coop tools rebuild .
```

New sessions pick it up automatically. Editing the Dockerfile changes the image
tag, so the next session rebuilds on its own — `rebuild` is just how you do it
now, with the build output in front of you.

**Don't add `grep`, `ls`, `cat`, `find` or `rg` to a manifest.** Each shim costs
~30–80ms of container startup, which is nothing for `python3` and ruinous in a
loop — and claude uses its own `Grep`/`Glob` tools for that work anyway, which
run on the host regardless.

**Four names are never shimmed, whatever a manifest says**: `tmux`, `docker`,
`podman` and `coop`. These are what coop itself runs by bare name from inside a
monitored session, so a shim for one doesn't fail loudly, it quietly reroutes
coop's own plumbing — the container's `tmux` talking to the host's tmux server
across a protocol it doesn't speak (which would take the status column and the
arbiter down to title-only with nothing logged anywhere), or a shimmed engine
calling `coop tools exec`, which resolves the engine through the same `PATH` and
finds the shim again, until the process limit. Installing them in the image is
fine and sometimes useful; putting them on the session's `PATH` is not. coop
drops those lines when it reads the manifest.

## Networks and host paths: `.coop/toolbox.json`

Two things a Dockerfile can't state:

```json
{
  "network": "sprocket-v2_default",
  "mounts": ["~/.local/bin:rw", "~/.aws:ro"]
}
```

**`network`** joins the container to an existing docker network — this is how a
repo's toolbox reaches the mongo and redis from its own `docker-compose.yml`.
Find the name with `docker network ls`; for compose it's usually
`<project>_default`.

**`mounts`** are extra host paths the container can see. Rules:

- A mount appears at the **same path** it has on the host. There's no
  `src:dst` form — that's deliberate, and it's why absolute paths in output
  always work.
- **Read-only unless you write `:rw`.**
- `~` expands to your home directory.
- `~/.config/coop` and `~/.local/state/coop` are refused, along with any parent
  of them (so `~` is refused too) and anything *inside* them. The inside case is
  the sharper one: `~/.local/state/coop/toolbox/<slug>/bin` holds another repo's
  shims, which are `#!/bin/sh` scripts that run on the host the moment that
  repo's session calls `python3`.

Mount `~/.local/bin:rw` if the repo builds something you install there —
otherwise the build succeeds and the binary vanishes (see *Gotchas*).

## Going all-in: builds and tests in the container

The rule is **all-in or all-out per repo, never split.** Either the repo's whole
toolchain is in the container or none of it is. Half-and-half is how you get a
binary written somewhere you'll never look.

coop itself is all-in. Its overlay:

```dockerfile
FROM coop-tools:base
COPY --from=golang:1.26-bookworm /usr/local/go /usr/local/go
RUN apt-get update && apt-get install -y --no-install-recommends tmux \
    && rm -rf /var/lib/apt/lists/*
ENV PATH=/usr/local/go/bin:$PATH CGO_ENABLED=0
RUN printf '%s\n' go gofmt >> /etc/coop/tools
```

Its `.coop/toolbox.json` is one line — `{ "mounts": ["~/.local/bin:rw"] }` —
because `go build -o ~/.local/bin/coop` has to land somewhere you can run it
from.

`CGO_ENABLED=0` matters whenever a repo builds something that runs on the host:
it makes the binary static, so a debian-slim build runs on your Ubuntu without
libc coupling.

The payoff for coop specifically is the pinned `tmux` the suite runs against.
coop has to absorb 3.4 vs 3.5 differences and a host can only ever have one
tmux, so the version becomes a line in a file that travels with the repo instead
of a property of whoever's machine it was checked out on — the apt one (3.3a on
bookworm) today, whatever that line says tomorrow. Note it is installed and
**not** in the manifest: `tmux` is one of the four reserved names above, because
coop's own hooks run `tmux` from inside every monitored session.

Three things stay on the host no matter what:

1. **Running coop** — it attaches to the host tmux server. Build in the
   container, run on the host.
2. **`scripts/toolbox-smoke.sh`** — it tests the toolbox and needs a docker
   socket, which is never mounted.
3. **Your editor's language server.** `gopls` runs on the host and wants a host
   Go. That's fine; the two don't interfere.

## Commands

| Command | What it does |
|---|---|
| `coop tools ls` | Running toolbox containers |
| `coop tools up [repo]` | Start one now instead of on first use |
| `coop tools shell [repo]` | Interactive shell inside it |
| `coop tools stop [repo]` | Kill it (the next command restarts it) |
| `coop tools rebuild [repo]` | Rebuild the image, build output on your terminal |
| `coop tools prune` | Remove stopped containers and superseded images |

`[repo]` defaults to the working directory. `coop tools exec` also exists, but
it's what the shims call — you shouldn't need it.

## Global settings

In `~/.config/coop/config.json`:

```json
{
  "toolbox": {
    "enabled": true,
    "engine": "docker",
    "idle_timeout": "30m"
  }
}
```

All three are optional. `"enabled": false` stops new sessions getting shims;
`"idle_timeout": "0"` means containers never exit on their own.

A setting only takes effect for sessions started afterwards. Shims are files on
disk and the `PATH` prefix is already baked into a running session's command, so
sessions that are already up keep calling `coop tools exec` — and when it can't
resolve usable settings (the toolbox turned off, an `idle_timeout` that stopped
parsing, a `config.json` that stopped parsing at all) it runs **the host's copy
of the tool** instead, silently, exactly as the session would have before the
toolbox existed. It only complains when there is no host copy to fall back on.
Container failures are still loud: they name `coop tools rebuild`.

## Gotchas

**`$HOME` inside the container is not your home.** It's a coop-owned per-repo
directory. So `go install`, `pip install --user`, `npm i -g` and `cargo install`
all succeed, work for the rest of that command, and are then invisible from the
host — forever. Only installs targeting a declared mount reach you.

**Your shell's environment doesn't come with you.** The container gets `TERM`,
`LANG`, `LC_ALL`, `LC_CTYPE` and `TZ` and nothing else — no `API_TOKEN` you
exported, no `AWS_PROFILE`. That's deliberate: a variable a tool needs should be
named in the overlay (`ENV`) or read from a mounted config directory, so it
travels with the repo instead of depending on which shell coop happened to be
started from.

**The base image ships no compilers.** Committing a repo's toolchain to the
container is a decision you make in the overlay, not a default you get handed.

**A container is per repo, shared by every session on it.** Two sessions in the
same repo use one container; two checkouts of the same project get two.

**Files the container writes are yours**, not root's — it runs as your uid. But
if a repo is *not* all-in, be wary of anything writing environment-coupled
artifacts into the tree (`.venv`, `node_modules`): those are built against the
container's runtime and will confuse a host toolchain that then reads them.

**This is not a security boundary.** The repo is mounted read-write, the
container runs as you, `/tmp` is shared. Anything with code execution inside can
rewrite the repo. The docker socket is never mounted, which is the one line that
would make it much worse. What you get is a reproducible toolchain and a clean
host — not a sandbox.

## Turning it off

Per machine: `"toolbox": {"enabled": false}` in `config.json`. New sessions get
no shims; sessions already running fall through to the host's tools (above).

Per repo: delete `.coop/tools.Dockerfile`. The repo falls back to the base
image; deleting `.coop/` entirely just means the base image with no extras.

To reclaim disk: `coop tools prune`.
