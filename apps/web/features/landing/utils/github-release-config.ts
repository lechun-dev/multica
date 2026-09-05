const DEFAULT_REPOSITORY = "lechun-dev/multica";
const RELEASE_LOOKBACK_LIMIT = 20;

/**
 * 2026-08-26 coder(lq): Keep the release repository configurable so private
 * deployments can point the download page at their own GitHub repository
 * without changing the landing-page components.
 */
export function getGithubReleaseRepository(): string {
  return (
    process.env.MULTICA_GITHUB_RELEASE_REPOSITORY?.trim() || DEFAULT_REPOSITORY
  );
}

export function getGithubReleasesApiUrl(): string {
  // Keep enough history to cross short beta trains and reach the latest
  // stable release. GitHub's list endpoint is ordered newest-first, and the
  // download page intentionally ignores prereleases.
  return `https://api.github.com/repos/${getGithubReleaseRepository()}/releases?per_page=${RELEASE_LOOKBACK_LIMIT}`;
}

export function getGithubReleasesPageUrl(): string {
  return `https://github.com/${getGithubReleaseRepository()}/releases`;
}
