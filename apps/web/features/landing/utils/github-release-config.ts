const DEFAULT_REPOSITORY = "lechun-dev/multica";

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
  return `https://api.github.com/repos/${getGithubReleaseRepository()}/releases?per_page=5`;
}

export function getGithubReleasesPageUrl(): string {
  return `https://github.com/${getGithubReleaseRepository()}/releases`;
}
