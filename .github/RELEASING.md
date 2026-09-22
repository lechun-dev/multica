# Release runbook

## Normal release

Release from a reviewed commit on `main` by creating and pushing a new semantic
version tag such as `v0.18.4`. The Release workflow intentionally has no manual
trigger: a tag push is the only event that can publish binaries, Homebrew
formulae, and container images.

Before creating the tag, add the same base version to the changelog in all four
locale files under `apps/web/features/landing/i18n/`. For example, the stable
tag `v0.4.82` requires a `0.4.82` entry in `en.ts`, `ja.ts`, `ko.ts`, and
`zh.ts`. Prerelease tags do not require these product notes because the final
summary is prepared for the stable release. Run
`node scripts/check-release-changelog.mjs v0.4.82` locally to verify it.

A test or beta release does not have to originate from `main`; only a stable
release does. Each named deployment workflow uses the selected tag as its image
version, so release preparation does not maintain a second version list.

The verification job requires those changelog entries for stable releases,
then runs the Go tests and `govulncheck` before any publishing job starts. The
stable changelog check and vulnerability checks are fail-closed by default.

For a stable tag, GitHub Release notes are generated automatically from every
non-merge commit between the previous stable tag and the new stable tag. Beta
and release-candidate tags are deliberately excluded as comparison bases. Both
the CLI and private desktop publishers apply the same generated body, so a
retry or a different job completion order cannot leave a partial changelog.
The localized entries above remain the curated product summary shown inside
MissionOS; they are not used as the complete GitHub Release audit trail.

## Test / prerelease release

To distribute a build to selected testers without showing it on the public
download page, use a semver prerelease tag such as `v0.4.70-beta.1`:

1. Start from the version after the latest stable release. If `v0.4.85` is
   already stable, the next preview line is `v0.4.86-beta.1`, followed by
   `v0.4.86-beta.2`; never create another `v0.4.85-beta.*` tag. The release
   checks reject a prerelease whose base version is not newer than the latest
   stable tag.
2. Create and push the tag from the reviewed commit. Beta and test tags may
   point to a non-`main` branch; the eventual stable tag must point to the
   reviewed commit on `main`.
3. The release workflows mark tags containing a suffix (`-beta.1`, `-rc.1`,
   etc.) as GitHub **Pre-release** and do not mark them **Latest**.
4. Deploy the staging private environment first with the
   `Deploy Multica Staging` workflow. Select the release tag under **Use
   workflow from**; that tag is also the image version. Then let desktop
   prerelease builds point their API, Web, and WS endpoints at that staging
   backend.
5. Give testers the direct GitHub Release URL. Do not add the URL to the
   website or stable install instructions.
6. Testers can download the CLI archive and run `missionos version` (or the
   compatible `multica version`) against the normal server.
7. After validation, create the corresponding stable tag (for example
   `v0.4.70`). That stable release becomes **Latest** and is then picked up by
   the website and automatic update checks.

The repository is public, so this is a visibility/channel separation rather
than access control: anyone who obtains the prerelease URL can still download
its assets. Do not put secrets or production-only data in a prerelease build.

## Deployment environment policy

All three deployment workflows derive the immutable image tag from GitHub's
**Use workflow from** selection. Select the version tag once and run the
workflow; there is no separate image-version field. Branches, `latest`, commit
SHA tags, release candidates, and other unsupported suffixes are rejected
before the workflow connects to a deployment host.

The manual workflow must exist on the repository's default branch for GitHub to
expose the **Run workflow** button, but the selected release and its application
code do not have to come from that branch. Test and beta deployments may select
tags created from their release branch.

| Workflow | Allowed tags |
| --- | --- |
| `Deploy Multica Production` | Stable only: `vX.Y.Z` |
| `Deploy Multica Staging` | Stable or beta: `vX.Y.Z`, `vX.Y.Z-beta.N` |
| `Deploy Multica Test` | Stable, beta, or test: `vX.Y.Z`, `vX.Y.Z-beta.N`, `vX.Y.Z-test.N` |

Test deployment is intentionally explicit. Publishing the mutable `latest`
images from `main` no longer triggers a test deployment automatically.

## Emergency vulnerability-scan bypass

Use the bypass only when `govulncheck` itself or its live vulnerability database
is unavailable, or when maintainers have documented a confirmed false positive
that blocks an urgent release. Never use it to publish a release with an
unresolved reachable vulnerability.

1. Record the reason and maintainer approval in the release issue or pull
   request, and confirm no other release is in progress.
2. In **Settings → Secrets and variables → Actions → Variables**, set the
   repository variable `ALLOW_VULN_BYPASS_FOR_TAG` to the exact release tag,
   for example `v0.18.4`.
3. Re-run the failed Release workflow for that tag. A different tag, an empty
   value, or any typo keeps the scan enabled.
4. Confirm the verification log contains the explicit bypass warning and retain
   the workflow URL in the incident record.
5. Delete `ALLOW_VULN_BYPASS_FOR_TAG` immediately after the release run
   completes. The tag-scoped value prevents a concurrent release with another
   tag from inheriting the bypass.

Every Go binary retains its compiler version in the standard Go build metadata;
use `go version -m <binary>` when auditing a downloaded release artifact.
