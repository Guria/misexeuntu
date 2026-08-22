# misexeuntu

Published as `ghcr.io/guria/misexeuntu:latest` (built by GitHub Actions on
every push to `main` and rebuilt weekly without cache; pin a build with the
commit-sha tag).

misexeuntu is a fork of [exeuntu](http://ghcr.io/boldsoftware/exeuntu), the
default base image for [exe.dev](https://exe.dev/), for machines whose software
is owned by a dotfiles installer rather than by the image.

- **mise is the only tool manager.** Coding agents and runtimes (claude, codex,
  pi, gh, uv, node) are not baked in; the machine's mise-managed dotfiles own
  them. `exeuntu update <agent>` stays available for manual installs.
- **Self-materializing.** At boot, `exeuntu materialize` asks the reflection
  endpoint (`https://reflection.int.exe.xyz/integrations`) for a github-type
  integration whose comment is `dotfiles`, clones it keylessly into
  `~/src/<host>/<owner>/<repo>`, and runs its unattended installer
  (`script/setup`). Unreachable reflection or no matching integration skips
  cleanly, so the image also works outside exe.dev.
- At boot the unit waits for reflection to answer (up to ~5 minutes) and materializes the moment the network is up; an hourly timer is only a backstop. Converge by hand with
  `systemctl start exeuntu-materialize.service` or `exeuntu materialize --force`.
- **Capabilities live in dotfiles features, not the image.** No baked browser,
  docker, build toolchain, media tools or tailscale (~1 GB combined, measured
  unused on real hosts); the dotfiles repo's `feat-*` bundles reinstall them
  on hosts that carry the feature (`feat-browser` defaults on for `exe.xyz`).

---

exeuntu is available at http://ghcr.io/boldsoftware/exeuntu

exeuntu is the default base image for [exe.dev](https://exe.dev/). It is kitted-out
for developers, based on ubuntu24.04, and includes systemd.

We believe that minimal containers make for terrible developer (and agent)
experiences, so exeuntu includes a lot of stuff, mostly from apt.

You can build exeuntu with Docker, but running it, including systemd,
is difficult with Docker.

