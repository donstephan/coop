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

### The short way: `/coop:init`

In any session coop started, run:

```
/coop:init
```

Claude reads the repo's `go.mod`/`package.json`/`pyproject.toml`, its
`docker-compose.yml` and its CI config, asks you one question — all-in or
tools-only (see *Going all-in* below) — and writes `.coop/tools.Dockerfile`
and `.coop/toolbox.json`. It stops there and hands you the
`coop tools rebuild .` to run yourself.

It writes nothing in `.claude/`: the permission grants come from the `commands`
block, and coop injects them itself (see *Permissions* below). They apply from
your next session, not the one you ran the skill in.

The skill ships inside the coop binary and is injected only into sessions coop
launches; a `claude` you started yourself won't have it. `COOP_PLUGIN=0` or
`-plugin=false` turns the injection off.

The rest of this section is what it writes, and what to write by hand.

### By hand

Create `.coop/tools.Dockerfile` in the repo:

```dockerfile
FROM coop-tools:base

RUN pip install --no-cache-dir pymongo redis boto3 tabulate
RUN apt-get update && apt-get install -y --no-install-recommends \
        postgresql-client \
    && rm -rf /var/lib/apt/lists/*
```

Then declare which commands go on the session's `PATH`, in
`.coop/toolbox.json`:

```json
{ "commands": { "python3": {}, "pip3": {}, "jq": {}, "curl": {}, "psql": {} } }
```

**`commands` is the complete list, not an addition to one.** Declaring it
replaces the image's own manifest, so list the base tools you rely on too —
that's the point: one file tells you everything this repo shims. Omit the block
entirely and you get the image's manifest instead, which is the zero-config
path and still works.

The older way — appending to `/etc/coop/tools` in the Dockerfile — is still
supported and is what an image uses to declare itself:

```dockerfile
RUN printf '%s\n' psql >> /etc/coop/tools
```

Prefer `commands` in a repo you're setting up by hand. It's explicit, it's
where `"allow": true` lives (see *Permissions* below), and editing it doesn't
rebuild the image.

Two things to know:

- **`FROM coop-tools:base` is always right.** coop keeps that tag pointing at
  the current base image, so you never write a hash.
- **Declared is not the same as installed.** `pip install pymongo` makes it
  importable from an already-declared `python3`; you add a `commands` entry
  only for a new *command* a session runs by name. Install without declaring
  when a tool is only needed inside the image.

**coop checks the two agree.** Declaring a command the image can't resolve
would otherwise give you a shim that exists — and is therefore granted, if you
marked it `"allow": true` — which then dies at exec on `executable file not
found`. So coop probes the image and names them:

```
$ coop tools rebuild .
warning: declared but not in the image: psql — their shims will fail at exec;
install them in .coop/tools.Dockerfile or drop them from "commands" in
.coop/toolbox.json
rebuilt /home/user/sprocket-v2
```

It's a warning, not a failure: the shims are still written, because quietly
skipping one would hand that command to the host, which is the thing declaring
it was meant to stop. On session create the same line goes to
`~/.local/state/coop/toolbox/build.log` instead, since nobody is watching.
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

## Permissions: stopping the prompts

A shim doesn't change the command string. Claude types `mongosh --eval ...`,
the shim intercepts it on `PATH`, and Claude Code's permission layer only ever
sees `mongosh --eval ...`. So the way to stop being prompted for a
containerized tool is to allowlist the **bare name** — not a
`docker compose exec` prefix.

**You don't write that rule anywhere.** Mark the command in the `commands`
block and coop writes it for you:

```json
{
  "commands": {
    "go": {},
    "gofmt": {},
    "psql": {},
    "python3": { "allow": true }
  }
}
```

- **Being declared is what shims a command.**
- **`"allow"` is the only route to a no-prompt grant**, and it is
  default-deny. Everything else declared is shimmed and still prompts.

`"allow": true` grants the whole command. A **list** grants one rule per
entry and leaves everything else prompting:

```json
{
  "commands": {
    "go":        { "allow": ["build", "test", "vet", "mod"] },
    "terraform": { "allow": ["plan", "validate"] },
    "gofmt":     { "allow": true }
  }
}
```

Be clear-eyed about what that buys, because it differs by tool:

- **On a compiler or interpreter it does not restrict anything.** `go test`
  builds and runs whatever is in the tree, and a test that shells out to
  `go install` gets there regardless. Same for `python3`, `node`, `make`.
- **What it does buy is where the prompt lands**, and that is worth having.
  `go install` succeeds, works for the rest of that command, and writes into a
  container `$HOME` you will never look in (see *Gotchas*). Granting `build`,
  `test`, `vet` and `mod` puts your inner loop on rails and makes the four
  surprising verbs stop and ask.
- **On a service CLI it is a real line.** `terraform plan` cannot become
  `terraform apply`, and `gh pr list` cannot become `gh pr merge`. Granting the
  read side while the write side prompts means what it looks like it means.

An entry is an argument prefix, matched ahead of the rest of the command line,
so `"build"` covers `go build -o ~/.local/bin/coop ./cmd/coop`. Entries are
plain words, paths and dashes; anything carrying quotes, `*` or shell
metacharacters is dropped, and dropping one costs you a prompt rather than the
file.

At session create, coop writes `Bash(python3 *)` into a settings file under its
own state directory — beside that repo's shims — and launches the session with
`--settings` pointing at it. Nothing lands in your repo.

**That's the whole reason there's no permission file to commit.** Putting the
rule in `.claude/settings.json` is the trap: that file is committed, so it also
reaches a `claude` somebody starts outside coop — where there is no shim, and
`Bash(python3 *)` now means "run the host's python3, unattended, no prompt."
The grant was made on the assumption "bare name = container", and committing it
carries it somewhere that assumption doesn't hold. An injected file reaches
exactly the sessions coop launched, which are exactly the sessions with the
shims.

Coop goes one step further and derives the rules from the shims that **exist**,
not from what the repo declared. A first session on a fresh clone launches
while the image is still building, so it gets no grants; the next one gets them
all. A grant can never arrive before the tool it grants.

**Both take effect on the next session.** Shims and grants are resolved when a
session is created, so after editing `.coop/toolbox.json` — or running
`/coop:init` — the session you're in keeps prompting until you start a new one.
`coop tools rebuild .` refreshes the shims immediately, but not the grants of a
session that has already launched.

To see what a new session would get, without starting one:

```
$ coop tools grants
Bash(go build *)
Bash(go test *)
Bash(gofmt *)
note: grants apply to sessions started from now on, not to running ones
```

The rules go to stdout and everything explaining them to stderr, so
`coop tools grants | wc -l` works. If a command you marked `"allow"` is missing
from the list, that's the point of the command: it says so, and why —
usually that its shim isn't written yet, so the grant is withheld until
`coop tools rebuild .` has run.

Two things this is not. It doesn't inspect anything at call time — a grant is
exactly as narrow as the rule, and nothing re-reads the command as it runs. And
it is not a boundary: the repo is mounted read-write and the container runs as
you. It keeps an allow rule from meaning something you didn't intend; it
doesn't contain anything.

A repo that already has its own `Bash(...)` rules committed keeps them.
`/coop:init` won't touch `.claude/` at all — if you have rules there from
before the toolbox, they're yours to keep or remove, and they carry the caveat
above.

## Networks and host paths: `.coop/toolbox.json`

Besides `commands` above, two things a Dockerfile can't state:

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
- **A path that doesn't exist on this host is skipped, not mounted.** docker
  doesn't fail on an absent bind source — it creates the path as root and
  mounts it empty, leaving a directory only `sudo` can remove and a tool that
  reads nothing out of it. So a mount whose source isn't there is dropped, and
  `coop tools up|shell|rebuild` prints one line naming what it skipped. The
  shims stay quiet about it.

That last rule is what lets one committed `toolbox.json` serve **two people
whose checkouts live in different places**: list both paths, and each host
mounts the one it has.

```json
{ "mounts": ["~/Documents/work/sprocket-data:ro", "~/src/sprocket-data:ro"] }
```

Mounts land at their host path, so the directory is at a *different* absolute
path for each of you — whatever reads it has to find it relatively (`../sprocket-data`
from the repo) rather than by hardcoded absolute path.

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

Its `.coop/toolbox.json` declares the three commands and the one mount that
`go build -o ~/.local/bin/coop` needs to land somewhere you can run it from:

```json
{
  "mounts": ["~/.local/bin:rw"],
  "commands": {
    "go":    { "allow": ["build", "test", "vet", "mod"] },
    "gofmt": { "allow": true },
    "jq":    {}
  }
}
```

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
| `coop tools home [repo]` | Print the container's `$HOME` for that repo |
| `coop tools grants [repo]` | Print the permission rules a new session would get |

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

The path itself is honest: that directory is bind-mounted at its own host path,
so what a tool prints inside is openable outside, verbatim. It just isn't the
home you expected. Two ways to find it:

```
coop tools home          # prints the path, so `ls "$(coop tools home)"` works
```

and a `README.coop-toolbox` sitting in the directory, which says which repo
owns it and how to make an install reach the host — deliberately not a dotfile,
since the point is to be seen by the `ls` that brought you there.

**The session is told this up front.** A shim is invisible by design — claude
types `go build` and that is exactly what runs — which is right until something
fails, when docker's `executable file not found` reads as a coop bug. So in
sessions coop launches, coop injects a short note listing the containerized
commands and naming the container `$HOME`. It arrives on every `SessionStart`,
including after a compaction, so it is still there in a long session. Nothing is
written to your repo to make this happen, and nothing reaches a `claude` you
started outside coop — where there are no shims, and the note would be false.
If the note is missing, the shims aren't written yet (or the build failed):
check `~/.local/state/coop/toolbox/build.log`.

**Your shell's environment doesn't come with you.** The container gets `TERM`,
`LANG`, `LC_ALL`, `LC_CTYPE` and `TZ` and nothing else — no `API_TOKEN` you
exported, no `AWS_PROFILE`. That's deliberate: a variable a tool needs should be
named in the overlay (`ENV`) or read from a mounted config directory, so it
travels with the repo instead of depending on which shell coop happened to be
started from.

**Install the new coop before adopting new `toolbox.json` syntax.** The shims
call whatever `coop` is on your `PATH`, and a repo config that binary can't
parse fails loud — so adopting a field or form your installed coop predates
stops *every shimmed command in that repo* with a parse error until you
rebuild. An `"allow": ["build", "test"]` list read by a coop that only knew
`"allow": true` takes out `go` itself, which is also what you need to rebuild
with; recover with the host's copy by absolute path, or by reverting the file.
Order is: `coop tools rebuild .` and a fresh binary first, edit second.

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
