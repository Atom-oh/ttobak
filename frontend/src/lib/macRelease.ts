export const MAC_INSTALL_COMMAND = 'curl -fsSL https://ttobak.atomai.click/mac/install.sh | sh';

const SEMVER = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;

/** Returns the published Mac app version from a latest.json manifest, or null when invalid. */
export function publishedVersion(manifest: unknown): string | null {
  if (typeof manifest !== 'object' || manifest === null) return null;
  const version = (manifest as { version?: unknown }).version;
  return typeof version === 'string' && SEMVER.test(version) ? version : null;
}
