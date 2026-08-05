# .coop/tools.Dockerfile
#
# coop is all-in: its whole toolchain lives here, not on the host. See the
# "all-in or all-out" rule in CLAUDE.md's toolbox section and docs/toolbox.md.
#
# tmux is installed but never shimmed. It is here so the suite can run
# against a declared tmux inside the container — coop absorbs 3.4 vs 3.5
# differences (vis(3)-escaped -F output, absent allow-set-title) and a
# host can only ever have one, so pinning it here makes the version a
# property of the repo rather than of whoever's machine it was checked
# out on. What bookworm ships is the pin today (3.3a).
#
# It stays off the manifest because coop's own plumbing runs "tmux"
# through PATH from inside a monitored pane: a shim would put this 3.3a
# in front of the host server's protocol, and since coop hook is silent
# and always exits 0, the hook status tier and the arbiter would go
# title-only with nothing logged. ParseManifest drops it either way —
# this line just does not ask.
FROM coop-tools:base

COPY --from=golang:1.26-bookworm /usr/local/go /usr/local/go

RUN apt-get update && apt-get install -y --no-install-recommends tmux \
    && rm -rf /var/lib/apt/lists/*

# CGO_ENABLED=0 is load-bearing: it makes the binary fully static and
# switches Go to its pure-Go user lookup, reading /etc/passwd directly,
# so a binary built on debian-slim runs on the host with no libc
# coupling. Without it os/user links glibc NSS — and internal/hub's
# judgeHome and internal/toolbox's userHome both depend on that lookup.
ENV PATH=/usr/local/go/bin:$PATH \
    CGO_ENABLED=0

RUN printf '%s\n' go gofmt >> /etc/coop/tools
