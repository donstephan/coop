---
name: init
description: Use when setting up coop's toolbox for a repo - reads the repo's toolchain, asks all-in vs tools-only, and authors .coop/tools.Dockerfile and .coop/toolbox.json, whose "allow" flags coop turns into the Bash permission grants it injects into the sessions it launches. Triggers on "set up coop", "coop init", "initialize the toolbox", "allowlist the container tools", or a repo with no .coop/ whose tooling should come from a container.
---

# Initialize a repo's coop toolbox

Author `.coop/tools.Dockerfile` — and `.coop/toolbox.json` when there is
something to put in it — for the repo in the working directory, so every coop
session on it gets that repo's tooling from a container instead of off the host.

Everything downstream follows from those two files. The commands a repo
declares are the commands coop shims, and the ones it marks `"allow": true` are
the ones coop grants without a prompt, in settings it injects itself. **You
write nothing in `.claude/`** — see step 5.

**Read `references/toolbox.md` in this skill's directory first.** It is the full
guide and the authority on every rule below; this file is only the procedure.

## Steps

### 1. Check what's already there

If `.coop/tools.Dockerfile` exists, do not overwrite it. Report what it installs
and what it shims, then ask whether to extend it or stop. A repo with only
`.coop/toolbox.json` is fine to add a Dockerfile to.

### 2. Read the repo

Look for what states the toolchain, not what merely hints at it:

- `go.mod`, `package.json`, `pyproject.toml`, `Gemfile`, `Cargo.toml` — language
  and version
- lockfiles — whether versions are pinned, which tells you how much a container
  buys here
- `docker-compose.yml` — service CLIs the repo will want (`psql`, `mongosh`,
  `redis-cli`) and the network name to join
- CI config — the most honest statement of what this repo needs to build and
  test, and usually the version to match
- `Makefile`, `scripts/` — the commands a person actually runs

### 3. Ask all-in or tools-only

This is the user's call, not yours. Never infer it. Report what you found, then
ask, in these terms:

- **All-in** — the compiler/runtime goes in the overlay and builds and tests run
  in the container. The host keeps no toolchain for this repo.
- **Tools-only** — just the service CLIs and utilities (`psql`, `jq`); the host
  keeps the compiler.

Say which one the repo looks like and why, but wait for the answer. The rule is
all-in or all-out per repo — a split toolchain is the failure mode the toolbox
exists to prevent, so do not propose "Go on the host, `go test` in the
container".

### 4. Write the files

`.coop/tools.Dockerfile` installs the tooling. `.coop/toolbox.json` declares
which commands the repo actually puts on the session `PATH`:

```json
{
  "network": "sprocket_default",
  "commands": {
    "go": {},
    "gofmt": {},
    "psql": {},
    "python3": { "allow": true }
  }
}
```

Three rules, and they are the whole model:

- **`commands` is the complete list.** It replaces the image's manifest rather
  than extending it, so a repo that declares commands and omits `python3` does
  not get `python3` from the base image. List everything the repo wants
  shimmed, including base tools it relies on (`python3`, `pip3`, `jq`, `curl`).
- **Being declared is what shims a command**, and a grant can only be written
  for something declared — so a rule for a command with no shim behind it is
  not something this file can express.
- **`"allow"` is the only way to a no-prompt grant**, and it is default-deny.
  Everything else declared is shimmed and still prompts, which is correct — a
  grant is a decision, not a side effect of installing a tool.

`"allow": true` grants the whole command; `"allow": ["build", "test"]` grants
one rule per entry (`Bash(go build *)`) and leaves the rest prompting. Reach
for the list form when it earns something, and say which of the two reasons it
is:

- **A prompt in a useful place.** On a compiler or interpreter this restricts
  nothing — `go test` runs whatever is in the tree, and a test that shells out
  to `go install` gets there anyway. But `go install` writes into a container
  `$HOME` invisible from the host, so granting `build`/`test`/`vet`/`mod` and
  letting `install`, `get` and `run` ask puts the friction on the surprising
  verbs instead of the inner loop. Do not describe this as a restriction.
- **A real line.** On a service CLI — `terraform plan` vs `apply`, `gh pr list`
  vs `merge`, `aws s3 ls` vs `rm` — the subcommands have different blast radii
  and neither can reach the other. Here narrowing means what it looks like.

An entry is an argument prefix (`"build"` covers `go build -o out ./cmd/x`).
Keep entries to plain words, paths and dashes.

Install a tool without declaring it when the repo only needs it *inside* the
image (a build dependency, something another tool calls). Declaring is for
commands a session runs by name.

Be sparing with `allow`. Propose it for the handful the repo genuinely runs on
a loop, say why for each, and leave the rest to prompt — the operator can
always add more, and every entry is a rule that must keep being true.

### 5. Write nothing in `.claude/` — coop injects the grants

**There is no permission file to write and no hook to install.** This is the
step people expect to exist, so it is worth knowing why it does not.

A shim does not change the command string: claude types `mongosh --eval ...`
and the permission layer sees exactly that, so the way to stop prompting for a
containerized tool is to allowlist the **bare name**. That is sound only while
a shim is on `PATH` — and a committed `.claude/settings.json` also reaches a
`claude` somebody starts outside coop, where there is no shim and the very same
rule means "run the host's copy, unattended, no prompt".

So the grant does not live in the repo. At session create coop writes
`Bash(<tool> *)` for each `"allow": true` command into a settings file under
its own state directory and injects it with `--settings`, which reaches exactly
the sessions coop launched — exactly the sessions that have the shims the grant
assumes. It derives those rules from the shims **actually written**, so a grant
cannot outrun the tool it grants.

What this means for you:

- **Never write `.claude/settings.json` or `.claude/hooks/*` for this.** Not
  through Write, and not through Bash — no `cat >`, no heredoc, no `jq ... >`.
  Those files are the harness's own control surface and Claude Code gates them
  on purpose; reaching one through Bash is circumventing a denial rather than
  satisfying it. There is nothing this skill needs in either of them.
- A repo that already has its own `Bash(...)` rules committed keeps them. They
  predate the toolbox and are the repo's business — read them if asked, but do
  not add to them and do not remove them.

### 6. Stop and hand back the rebuild

Do **not** run `coop tools rebuild`. Print a summary — what you installed, what
you shimmed, what you installed without declaring, and which commands you gave
`"allow": true` and why — and end with:

```
coop tools rebuild .
```

It takes minutes and its whole value is the build output on the user's terminal.

**Say plainly that shims and grants both take effect on the next session.**
The session you are in launched before these files existed: after the rebuild
its shims work, but the grants were resolved at launch, so declared commands
keep prompting until a new session starts. That is expected, not a fault, and a
user who is not told will read it as the setup having failed.

## Rules that bite silently

These are the ones where a wrong choice produces something that looks like it
worked. `references/toolbox.md` explains each.

- **`FROM coop-tools:base`** — always that tag, never a hash. coop keeps it
  pointing at the current base image.
- **Installed ≠ shimmed.** `RUN pip install pymongo` makes it importable from
  the already-shimmed `python3`. You append to `/etc/coop/tools` only for a new
  *command*.
- **Four names are never shimmed**: `tmux`, `docker`, `podman`, `coop`. Install
  them freely; putting them on the session `PATH` silently reroutes coop's own
  plumbing. coop drops those lines anyway — don't write them.
- **Don't shim `grep`, `ls`, `cat`, `find`, `rg`.** ~30–80ms of container
  startup each, and Claude Code's own `Grep`/`Glob` run on the host regardless.
- **The build context is `.coop/`, not the repo.** `COPY` can only reach files
  next to the Dockerfile.
- **`CGO_ENABLED=0`** whenever the repo builds a binary meant to run on the
  host, so a debian-slim build isn't coupled to the container's libc.
- **`$HOME` in the container is not the user's home.** `go install`, `npm i -g`
  and `pip install --user` land somewhere invisible from the host forever. If
  the repo installs a binary the user runs, mount the target: `"mounts":
  ["~/.local/bin:rw"]`.
- **Mounts are read-only unless `:rw`**, appear at the same path they have on
  the host, and cannot name `~/.config/coop` or `~/.local/state/coop`, any
  parent of them, or anything inside them.
- **`network` is explicit.** Read the name from `docker network ls` or the
  compose project; never guess it from a nearby `docker-compose.yml`.
- **The environment does not come with you.** The container gets `TERM`, `LANG`,
  `LC_ALL`, `LC_CTYPE`, `TZ` and nothing else. A variable a tool needs belongs
  in the overlay as `ENV`, so it travels with the repo.
