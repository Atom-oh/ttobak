/** Restore brackets that Markdown serializers encode in a valid IPv6 authority.
 * The path, query and fragment retain their exact bytes. Protocol filtering and
 * HTML sanitization remain the caller's responsibility.
 */
export function restoreMarkdownIPv6Authority(value: string): string {
  const match = /^(https?:\/\/)%5b([0-9a-f:.]+)%5d(?::([0-9]+))?(?=[/?#]|$)/i.exec(value);
  if (!match) return value;
  const authority = `${match[1]}[${match[2]}]${match[3] === undefined ? '' : `:${match[3]}`}`;
  const restored = authority + value.slice(match[0].length);
  try {
    const parsed = new URL(restored);
    if (!parsed.hostname.startsWith('[') || !parsed.hostname.endsWith(']')) return value;
    return restored;
  } catch {
    return value;
  }
}
