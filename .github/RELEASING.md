# Release runbook

## Normal release

Release from a reviewed commit on `main` by creating and pushing a new semantic
version tag such as `v0.18.4`. The Release workflow intentionally has no manual
trigger: a tag push is the only event that can publish binaries, Homebrew
formulae, and container images.

The verification job runs the Go tests and `govulncheck` before any publishing
job starts. The vulnerability scan is fail-closed by default.

## Test / prerelease release

To distribute a build to selected testers without showing it on the public
download page, use a semver prerelease tag such as `v0.4.70-beta.1`:

1. Create and push the tag from the reviewed commit on `main`.
2. The release workflows mark tags containing a suffix (`-beta.1`, `-rc.1`,
   etc.) as GitHub **Pre-release** and do not mark them **Latest**.
3. Give testers the direct GitHub Release URL. Do not add the URL to the
   website or stable install instructions.
4. Testers can download the CLI archive and run `missionos version` (or the
   compatible `multica version`) against the normal server.
5. After validation, create the corresponding stable tag (for example
   `v0.4.70`). That stable release becomes **Latest** and is then picked up by
   the website and automatic update checks.

The repository is public, so this is a visibility/channel separation rather
than access control: anyone who obtains the prerelease URL can still download
its assets. Do not put secrets or production-only data in a prerelease build.

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
