// Link extraction from Slack message text and a public-link filter. This is a
// privacy pre-filter run on the Mac; the server (`model.NormalizeUpdateURL`)
// remains the authority and this filter is at least as strict on its points.

/** Mirrors contracts/customer-updates/internal-hosts-v1.json (checked by a test). */
export const INTERNAL_HOST_SUFFIXES: readonly string[] = [
  'slack.com',
  'slack-files.com',
  'amazon.dev',
  'aws.dev',
  'a2z.com',
  'corp.amazon.com',
  'w.amazon.com',
  'quip-amazon.com',
  'amazon.work',
  'awsapps.com',
  'sharepoint.com',
  'salesforce.com',
  'force.com',
  'sim.amazon.com',
  'issues.amazon.com',
  'code.amazon.com',
  'phonetool.amazon.com',
  'broadcast.amazon.com',
];

const MAX_URLS_PER_MESSAGE = 20;
const MAX_URL_LENGTH = 2048;

// Either a Slack angle-bracket token (<target> or <target|label>, the target
// without whitespace) or a bare URL.
const TOKEN = /<([^<>|\s]*)(?:\|[^<>]*)?>|https?:\/\/[^\s<>]+/gi;
const SCHEME = /^https?:\/\//i;
// '>' can end a bare URL only as an escaped `&gt;`, which is never part of it.
const TRAILING = new Set(['.', ',', '!', '?', ']', ')', '>']);

function unescapeSlack(text: string): string {
  return text.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
}

function count(text: string, character: string): number {
  let total = 0;
  for (const c of text) if (c === character) total++;
  return total;
}

// Strips trailing punctuation from a bare URL; a ")" stays while it balances a "(".
function stripTrailing(url: string): string {
  let end = url.length;
  while (end > 0 && TRAILING.has(url[end - 1])) {
    const head = url.slice(0, end);
    if (url[end - 1] === ')' && count(head, ')') <= count(head, '(')) break;
    end--;
  }
  return url.slice(0, end);
}

/** Distinct http(s) URLs in a Slack message in order of appearance, at most `max` (default 20). */
export function extractUrls(text: string, max = MAX_URLS_PER_MESSAGE): string[] {
  const found: string[] = [];
  const seen = new Set<string>();
  for (const match of text.matchAll(TOKEN)) {
    let url: string;
    if (match[1] !== undefined) {
      if (!SCHEME.test(match[1])) continue;
      url = unescapeSlack(match[1]);
    } else {
      url = stripTrailing(unescapeSlack(match[0]));
    }
    if (!SCHEME.test(url) || seen.has(url)) continue;
    seen.add(url);
    found.push(url);
    if (found.length >= max) break;
  }
  return found;
}

function internalHost(host: string): boolean {
  return INTERNAL_HOST_SUFFIXES.some((suffix) => host === suffix || host.endsWith('.' + suffix));
}

// True when every dot-separated label is an integer (decimal or 0x hex): an IPv4 spelling.
function allLabelsInteger(host: string): boolean {
  return host.split('.').every((label) => /^(0x[0-9a-f]+|[0-9]+)$/i.test(label));
}

/** The trimmed URL when it is safe to upload as a public link, otherwise null. */
export function publicUrl(raw: string): string | null {
  if (typeof raw !== 'string') return null;
  const url = raw.trim();
  // Bytes, as the server counts them.
  if (url.length === 0 || Buffer.byteLength(url, 'utf8') > MAX_URL_LENGTH) return null;
  if (/[\s\u0000-\u001f\u007f-\u009f]/.test(url)) return null;
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return null;
  }
  if (parsed.protocol !== 'https:') return null;
  if (parsed.username !== '' || parsed.password !== '') return null;
  const host = parsed.hostname.toLowerCase().replace(/\.$/, '');
  if (host === '' || host.startsWith('[') || host.includes(':') || !host.includes('.')) return null;
  if (/^[0-9]+$/.test(host.slice(host.lastIndexOf('.') + 1)) || allLabelsInteger(host)) return null;
  if (internalHost(host)) return null;
  return url;
}
