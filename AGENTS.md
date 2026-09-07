# misexeuntu

A fork of [exeuntu](https://github.com/boldsoftware/exeuntu), the default VM image for [exe.dev](https://exe.dev/). Upstream is a fat developer VM with everything baked in. This fork is for machines whose tools are owned by a dotfiles installer, not by the image.

## Core tenet

**mise is the only tool manager.** Coding agents (claude, codex, pi, opencode) and runtimes (node, go, uv, gh) are not baked into the image. The machine's dotfiles own them through mise, materialized at boot by `exeuntu materialize`.

## Divergence from upstream

- **several GB lighter** on disk. Aggressive package removal does most of the work, with purges in follow-ups.
- **Self-materializing.** `exeuntu materialize` discovers the dotfiles repo via reflection, clones it keylessly, runs its installer. Boot blocks until reflection answers; hourly timer is a backstop.
- **Features are opt-in per host.** The dotfiles repo's `feat-*` bundles (feat-browser, feat-docker, feat-build, etc.) reinstall what was removed on hosts that opt in.
- **`exeuntu update <agent>` stays available** for manual installs outside the dotfiles flow.

## Key services

- `exeuntu-materialize.service` — runs at boot after network comes up; waits for reflection to answer before converging.
- `exeuntu-materialize.timer` — hourly backstop only; the real run is at boot.
- `exe-setup.service` — runs `/exe.dev/setup` on first boot (from upstream).
- `shelley.socket` / `shelley.service` — socket-activated agent (from upstream, modified).

## Layout

- `Dockerfile` — image definition, ubuntu base image. The CLI is built in a Go stage at the top.
- `cli/` — `exeuntu` Go CLI (module `github.com/guria/misexeuntu`). Commands: `materialize`, `configure`, `install`, `update`, `version`.
- `cli/internal/dotty` — dotfiles discovery, clone, and converge logic.
- `cli/internal/guestllm` — LLM integration configuration for coding agents.
- `pi-extension/` — pi agent extension; heavily modified from upstream (routing, catalog, gateway).
- `opencode-plugin/` — OpenCode plugin for exe.dev model discovery.
- `init-wrapper.sh` — PID 1 wrapper that sets up cgroups and starts systemd.
- `motd-snippet.bash` — interactive shell banner; shows materialize progress until first converge completes.
- `exeuntu-agents.md` — instructions for agents running inside the VM (not for agents working on this repo).

## Upstream merging

This repo merges from upstream periodically. Strategy so far: adapt when the upstream change is compatible, `git merge --strategy ours upstream/main` when it assumes our lean removals.

**How to merge:**

```bash
git fetch upstream
git merge upstream/main --no-ff
# resolve conflicts per the table below
git commit --no-edit
```

**Per-file decision table:**

| File / area | Conflict style | Decision rule |
|-------------|---------------|---------------|
| `Dockerfile` package installs | Usually clean — we removed what upstream keeps adding | Keep our removals; accept upstream's security fixes |
| `Dockerfile` Go builder stage | One-line version bumps | Accept — we still build the CLI |
| `cli/` | Shared code, genuine merge | Adapt to our lean base: upstream may reference baked tools we removed |
| `pi-extension/` | Heavy fork divergence | Bias toward ours; upstream's features may assume their fat image |
| `opencode-plugin/` | Shared | Accept upstream improvements |
| New baked package / agent install | Upstream adds, we removed | `-s ours` for the merge, or resolve to skip the hunk |
| Test fixes | Clean | Accept — tests are shared |
| Systemd services | Case-by-case | We have diverged services (materialize, shelley) |

**When the conflict is fundamental** — upstream adds something that conflicts with the lean philosophy (another baked agent, another baked runtime, anything that duplicates what mise owns) — use `git merge --strategy ours upstream/main` for that specific merge. The commit message says why ("not applicable to our lean image"), preserving the upstream parent so `git log` stays walkable.

## Building and testing

```bash
make build          # docker build -t ghcr.io/guria/misexeuntu:latest
cd cli && go test ./...
```

## Note for agents working on the image

- `exeuntu-agents.md` is what agents inside the VM see. This file is for agents working on the image itself.
- Do not add baked-in software to the Dockerfile. If you need a tool, it belongs in the dotfiles repo or a `feat-*` bundle.
- The Dockerfile comment style: multi-line blocks explaining *why* something was removed, not just *what*.
