# S6 Deprecation-Window Closures -- Operator Runbook (R1 repo)

**Scope:** governs the scheduled SHUTDOWN dates of the S1-* dual-accept
windows shipped in the R1 rename launched 2026-04-23.

**Governing plan:** `/home/eric/repos/plans/work-orders/work-r1-rename.md`
Phase S6.

## Summary table (this repo)

| Sub-phase | Date | Branch | Surfaces dropped |
|-----------|------|--------|------------------|
| S6-1 | 2026-05-23 (30d) | `claude/r1-s6-1-headers-drop-stoke` | Legacy X-Stoke-* outbound header emission. |
| S6-3 | 2026-07-23 (90d) | `claude/r1-s6-3-env-drop-stoke` | Legacy STOKE_* env fallback. |
| S6-4 | 2026-07-23 | `claude/r1-s6-4-symlink-drop-stoke` | `stoke` binary install + Homebrew `stoke` formula. |
| S6-6 | TBD | `claude/r1-s6-6-mcp-v2-stoke` | MCP stoke_* tool registrations (v2.0.0). |

---

## S6-4 -- Drop stoke binary + Homebrew stoke formula (2026-07-23)

**Parent branch:** `main` (carries the S2-3 dual-install install.sh and
S2-4 dual-brew-formula .goreleaser.yml this branch retires).

**Surfaces dropped:**

- `install.sh`:
  - Top-of-file banner updated -- script is now the "R1 installer
    (formerly stoke)".
  - `BINARY="stoke"` default flipped to `BINARY="r1"`.
  - `build_from_source` loop: the `stoke-bin` compile from
    `./cmd/stoke` is removed. `r1-bin` + `stoke-acp-bin` remain.
    (stoke-acp keeps its name because ACP is a protocol identifier,
    not product prose.)
  - Install loop: ordering + pair list flipped so `r1-bin:r1` +
    `stoke-acp-bin:stoke-acp` are the two binaries installed from
    source. Legacy `stoke-bin:${BINARY}` pair removed.
  - Prebuilt-archive `install_one stoke ...` line removed. Only
    `install_one r1 "${BINARY}" required` + `install_one stoke-acp
    stoke-acp optional` remain.
  - Final-line user prompt flipped from
    "Run 'stoke doctor' (or 'r1 doctor')" to "Run 'r1 doctor'".
- `.goreleaser.yml`:
  - The legacy `- name: stoke` brew entry (Homebrew formula
    `ericmacdougall/stoke/stoke`) deleted in full (30 lines).
  - The canonical `- name: r1` brew entry retained, with the
    `install:` block simplified -- `bin.install "r1"` reads the
    canonical build-id output directly (no longer symlink-renaming
    `stoke` to `r1`).
  - `commit_msg_template` for the retained formula unchanged.
- `internal/r1rename/s6_4_install_guard_test.go` (new):
  - Go regression guard with three tests:
    - `TestS64_InstallSh_NoStokeBinaryInstallStep` asserts the
      `install_one stoke` line is absent and the canonical
      `install_one r1` line is present.
    - `TestS64_InstallSh_NoStokeBinBuildFromSource` asserts the
      `stoke-bin` artifact identifier is gone and `r1-bin` is present.
    - `TestS64_Goreleaser_NoStokeBrewFormula` asserts the
      `- name: stoke\n` brew entry is absent and `- name: r1\n`
      remains.

**Additional cutover step (operator, NOT part of this branch):**

The `homebrew-stoke` tap repository lives outside this repo. On the
cutover day the operator must push a commit to that tap that marks
the `stoke.rb` formula deprecated:

```ruby
# Formula/stoke.rb
class Stoke < Formula
  deprecate! date: "2026-07-23", because: "renamed to r1; install with 'brew install ericmacdougall/stoke/r1'"
  # ... existing body preserved so installs still work read-only ...
end
```

Brew's `deprecate!` mechanism surfaces a visible warning to any user
who tries `brew install ericmacdougall/stoke/stoke`. A future tap
commit can escalate to `disable!` when usage counts reach zero.

Any apt repo `stoke` package is marked `retracted` on the same day
via whatever apt publisher is wired at that time (no nfpms surface
in `.goreleaser.yml` at-dispatch-time, so this step is
documentation-only here).

**Pre-cutover checklist (run the week of 2026-07-16):**

- [ ] Query Homebrew analytics for `ericmacdougall/stoke/stoke`
      install counts trend. Post a deprecation notice on the tap
      README one week ahead.
- [ ] Verify the S6-3 stoke branch (`claude/r1-s6-3-env-drop-stoke`)
      is coordinated for the same cutover day.
- [ ] Build + test matrix green on this branch.

**Cutover:**

```bash
cd /home/eric/repos/stoke
git checkout main
git pull --ff-only origin main
git merge --no-ff claude/r1-s6-4-symlink-drop-stoke \
  -m "chore(S6-4): drop stoke binary symlinks + retract Homebrew/apt stoke package"
git push origin main

# Tag + release via goreleaser.
git tag v<next>
git push origin v<next>
# goreleaser workflow publishes only the r1 binary + r1 brew formula.

# homebrew-stoke tap repo: push the deprecate! commit above.
```

**Rollback:**

```bash
cd /home/eric/repos/stoke
git revert --no-ff <S6-4-merge-sha> -m 1 \
  -m "revert(S6-4): reinstate stoke binary install + Homebrew formula"
git push origin main
```

The homebrew-stoke tap's `deprecate!` edit stays separate; remove it
if the rollback needs to fully reinstate stoke installability.

---

## S6-1 / S6-3 / S6-6

See the respective branches for their per-file diffs.

---

## Status at-dispatch-time (2026-04-24)

Branch scaffolded off `main` (which at-dispatch-time carries the
S2-1 Go module rename merged on PR #65). Not pushed, not merged.
