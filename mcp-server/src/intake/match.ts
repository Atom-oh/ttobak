// Deterministic account matcher for calendar intake. Pure: no I/O, no logging.
// Terms are normalized (NFKC, lowercase, collapsed whitespace). A term with
// Hangul matches as a substring; any other term needs a non-letter/digit
// boundary on both sides. Domains match exactly or as a subdomain.

export interface DirectoryAccount {
  accountId: string;
  name: string;
  aliases: string[];
  domains: string[];
}

export type MatchEvidence = 'title' | 'location' | 'domain';

export type MatchResult =
  | { kind: 'matched'; accountId: string; evidence: MatchEvidence[] }
  | { kind: 'candidate'; accountIds: string[] }
  | { kind: 'none' };

export interface MatchInput {
  title: string;
  location?: string;
  domains: string[];
}

export interface Matcher {
  match(input: MatchInput): MatchResult;
}

export const DEFAULT_OWN_DOMAINS = ['amazon.com'];

const MAX_CANDIDATES = 5;
const MIN_TERM_POINTS = 2;
const HANGUL = /\p{Script=Hangul}/u;
const LETTER_OR_DIGIT = /[\p{L}\p{N}]/u;

function normalize(s: string): string {
  return String(s ?? '').normalize('NFKC').toLowerCase().replace(/\s+/g, ' ').trim();
}

function normalizeDomain(s: string): string {
  return normalize(s).replace(/^@+/, '').replace(/\.+$/, '');
}

function domainMatches(domain: string, d: string): boolean {
  return domain === d || domain.endsWith('.' + d);
}

function containsTerm(text: string, term: string): boolean {
  if (!text) return false;
  if (HANGUL.test(term)) return text.includes(term);
  let from = 0;
  for (;;) {
    const i = text.indexOf(term, from);
    if (i < 0) return false;
    const before = [...text.slice(0, i)].pop();
    const after = [...text.slice(i + term.length)][0];
    if (
      (before === undefined || !LETTER_OR_DIGIT.test(before)) &&
      (after === undefined || !LETTER_OR_DIGIT.test(after))
    ) {
      return true;
    }
    from = i + 1;
  }
}

interface Compiled {
  accountId: string;
  terms: string[];
  domains: string[];
}

export function buildMatcher(accounts: DirectoryAccount[], ownDomains: string[]): Matcher {
  const own = ownDomains.map(normalizeDomain).filter(Boolean);
  const isOwn = (d: string) => own.some((o) => domainMatches(d, o));

  const compiled: Compiled[] = accounts.map((a) => ({
    accountId: a.accountId,
    terms: [...new Set([a.name, ...a.aliases].map(normalize))].filter(
      (t) => [...t].length >= MIN_TERM_POINTS,
    ),
    domains: [...new Set(a.domains.map(normalizeDomain))].filter((d) => d && !isOwn(d)),
  }));

  return {
    match(input: MatchInput): MatchResult {
      const title = normalize(input.title);
      const location = normalize(input.location ?? '');
      const attendees = [...new Set(input.domains.map(normalizeDomain))].filter(
        (d) => d && !isOwn(d),
      );

      const hits: { accountId: string; evidence: MatchEvidence[] }[] = [];
      for (const c of compiled) {
        const evidence = new Set<MatchEvidence>();
        if (c.terms.some((t) => containsTerm(title, t))) evidence.add('title');
        if (c.terms.some((t) => containsTerm(location, t))) evidence.add('location');
        if (attendees.some((d) => c.domains.some((cd) => domainMatches(d, cd)))) {
          evidence.add('domain');
        }
        if (evidence.size > 0) {
          hits.push({ accountId: c.accountId, evidence: [...evidence].sort() });
        }
      }

      const ids = [...new Set(hits.map((h) => h.accountId))].sort();
      if (ids.length === 0) return { kind: 'none' };
      if (ids.length === 1) {
        const evidence = [...new Set(hits.filter((h) => h.accountId === ids[0]).flatMap((h) => h.evidence))].sort();
        return { kind: 'matched', accountId: ids[0], evidence };
      }
      return { kind: 'candidate', accountIds: ids.slice(0, MAX_CANDIDATES) };
    },
  };
}
