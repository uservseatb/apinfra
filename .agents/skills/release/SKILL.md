---
name: release
description: Local-only release prep for apinfra — release notes, local git tag, and GoReleaser package build. Never commit, push, or publish to GitHub unless the user explicitly asks.
---

# Skill: Local Release Prep (notes + tag + build)

Use this skill when the user asks to prepare, build, or ship a new release, create a version tag, or generate release notes.

Authoritative project policy also lives in `AGENTS.md` → **Release Workflow**. Prefer that wording if anything conflicts.

## What the agent does (local only)

1. Show proposed version + Release Notes and **wait for confirmation**.
2. After confirmation and **before** creating or pushing the new tag: update `README.md` Installation (Option 1) so the GitHub Releases `.deb` curl URL uses the confirmed version (`.../download/vX.Y.Z/apinfra_X.Y.Z_amd64.deb`). Commit that bump (`docs: bump install link to vX.Y.Z`) so the upcoming tag includes it. Skip only if README already matches and is committed.
3. Create a **local** annotated git tag.
4. Build packages locally with GoReleaser into `dist/` (no publish).
5. Print Release Notes again and tell the user which tag / `.deb` files are ready.

## What the agent must NOT do

- Do **not** create commits unless the user explicitly asks — **exception:** after release confirmation, the README install-link bump commit is required before tagging.
- Do **not** `git push` (branch, tags, or anything else).
- Do **not** create or update a GitHub Release.
- Do **not** open PRs, dispatch GitHub Actions, or otherwise publish remotely.
- Do **not** run `goreleaser release` in a way that publishes (keep `release.disable: true` / use `--skip=publish` as needed).

The user pushes the tag/commits and uploads artifacts themselves.

## Pre-flight checks

Before tagging or building:

1. **Git status check:**
   - Run `git status --porcelain`. If there are uncommitted changes, **stop** and ask the user to commit or stash (do not commit for them unless they explicitly request it).
   - Confirm the current branch is `main` or `master` (if not — ask the user).
   - Prefer refreshing tags with `git fetch --tags` when network/auth works; if fetch fails, continue with local tags and warn the user.

2. **Code quality and dependencies:**
   - Run `go mod tidy` and check `git diff go.mod go.sum` for unexpected changes.
   - Run tests: `go test -race ./...`. If tests fail, abort the release and show the errors.
   - If `golangci-lint` is available in the project, run `golangci-lint run`.

## Version selection & Release Notes

1. Find the latest tag with `git describe --tags --abbrev=0` (if there are no tags, suggest `v0.1.0`).
2. Review commits since the last tag: `git log <last-tag>..HEAD --oneline`.
3. Based on Conventional Commits (`feat:`, `fix:`, `BREAKING CHANGE:`), propose the next version (patch, minor, or major).
4. Generate **Release Notes** in English following the project template below (match tone of prior GitHub releases).
5. Show the proposed version and the generated Release Notes to the user, and **always ask the user to confirm** before creating the tag.

### Release Notes template

Use this structure (adapt middle sections to the release: Features / Security / Bug Fixes / Improvements / Upgrade notes, etc.).

**Do not** start with `## apinfra vX.Y.Z` (or any title that repeats the version). GitHub / the chat already shows the version; notes begin with the summary sentence.

**Chat presentation (required):** Release Notes contain a nested ` ```bash ` block. When wrapping notes for the user in a fenced code block, use a **4-backtick** outer fence (` ````markdown ` … ` ```` `), not triple backticks. A triple-backtick outer fence closes at the bash block’s closing ` ``` `, so the README footer line drops out of the code block and renders as live Markdown.

Template body (what goes into the GitHub release / copy-paste block):

````markdown
One short summary sentence for the release.

### <emoji> <Section for this release>
* Bullet points of user-facing changes…

### 📦 Asset
* `apinfra_X.Y.Z_amd64.deb` (Ubuntu package)
* `apinfra_X.Y.Z_arm64.deb` (Ubuntu package)   # include only if that artifact is built/shipped

### 🛠️ Installation
Connect to your Ubuntu 22.04+ server and run:
```bash
# 1. Download the latest package version
curl -L -O https://github.com/uservseatb/apinfra/releases/download/vX.Y.Z/apinfra_X.Y.Z_amd64.deb

# 2. Update package indexes and install
sudo apt update
sudo apt install -y ./apinfra_X.Y.Z_amd64.deb
```

*For more details on configuration, check out the [README.md](https://github.com/uservseatb/apinfra/blob/main/README.md).*
````

Always include **Asset** and **Installation** with the exact version in filenames and the GitHub download URL. Prefer listing the primary `.deb` the user installs (amd64); add arm64 when that package is part of the release. Keep the italic README footer **inside** the notes (after the install bash block).

## After confirmation: README bump → local tag + build

1. **README install link (required before tag):**
   - In `README.md` → Installation → Option 1, set the download URL to the confirmed version:
     `https://github.com/uservseatb/apinfra/releases/download/vX.Y.Z/apinfra_X.Y.Z_amd64.deb`
   - Commit with a single-line subject like `docs: bump install link to vX.Y.Z` (allowed as part of the confirmed release; do this even when other commits were not requested).
   - Do **not** create the version tag until this commit is on the release branch (or README already matches and is committed).

2. **GoReleaser:**
   - Require `.goreleaser.yaml` / `.goreleaser.yml` in the repo root.
   - Optional dry-run before tagging: `goreleaser release --snapshot --clean`.
   - After the local tag exists, build versioned packages without publishing, e.g. `goreleaser release --clean --skip=publish` (or equivalent that respects `release.disable: true`).
   - Point the user at `dist/*.deb` (and related archives).

3. **Local tag only:**
   - Create an annotated tag: `git tag -a <NEW_VERSION> -m "Release <NEW_VERSION>"`.
   - **Do not** push the tag.

4. **Final output for the user:**
   - Print the final **Release Notes** in a clean, copy-pasteable Markdown code block using a **4-backtick** outer fence so the nested bash install block and the README footer stay inside the fence.
   - Remind them the tag is **local only**, list produced artifacts under `dist/`, and that they push / publish when ready.

## Safety rules

- **Never** overwrite existing tags (`git tag -f`) unless the user explicitly orders it.
- **Never** create the tag if tests, linter, or the packaging build failed (fix failures before tagging when practical).
- **Never** publish releases or push remotes from the agent unless the user explicitly asks in that turn.
