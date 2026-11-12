// `ttobak-mcp report slack-channels [--days N]`: the channels where the owner
// posted in the last N days (default 90), counted from the selected Slack
// connector's `search`. Only channel names and counts leave this module; DMs
// and group DMs are left out. It is the candidate list of the app's channel
// picker; report runs exclude what the owner unchecks.

import { enabled, openSession, type IntakeEnvironment, type Selection } from '../intake/run.js';
import { resultJson } from '../intake/mcp-client.js';
import type { IntakeStore } from '../intake/store.js';
import { ReportError } from './errors.js';

export interface SlackChannelsEnvironment extends IntakeEnvironment {
  /** Cancels the refresh (the CLI wires SIGINT/SIGTERM to it). */
  signal?: AbortSignal;
  /** Test seam: replaces the Slack connector's `search` call. */
  slackSearch?: (args: Record<string, unknown>) => Promise<unknown>;
  /** Test seam: replaces the pause between search calls. */
  sleep?: (ms: number) => Promise<void>;
}

const MAX_PAGES = 40;
// slack-mcp search allows 15 calls per minute.
const PAUSE_MS = 4000;
const SEARCH_TOOL = { name: 'search', properties: ['query', 'count', 'page', 'scope', 'sort'] };

function days(args: string[]): number {
  if (args.length === 0) return 90;
  const value = Number(args[1]);
  if (args.length !== 2 || args[0] !== '--days' || !Number.isInteger(value) || value < 1 || value > 180) {
    throw new ReportError('INVALID_ARGUMENT', '--days must be 1 to 180');
  }
  return value;
}

function since(now: Date, zone: string, back: number): string {
  const start = new Date(now.getTime() - back * 86_400_000);
  return new Intl.DateTimeFormat('en-CA', { timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit' }).format(start);
}

export async function slackChannels(args: string[], environment: SlackChannelsEnvironment, store: IntakeStore) {
  const after = since(environment.now(), environment.zone, days(args));
  let search = environment.slackSearch;
  let close = async () => {};
  if (!search) {
    const selected = enabled(store.read<Selection>('selection.json')?.slack);
    if (!selected) throw new ReportError('SLACK_NOT_SELECTED');
    const session = await openSession(environment, selected.host, selected.server, selected.pathDirs, [SEARCH_TOOL]);
    search = async query => resultJson(await session.call('search', query), 'search');
    close = () => session.close();
  }
  const sleep = environment.sleep ?? (ms => new Promise<void>(resolve => setTimeout(resolve, ms)));
  const counts = new Map<string, number>();
  let partial = false;
  try {
    for (let page = 1; ; page++) {
      if (page > MAX_PAGES) { partial = true; break; }
      if (page > 1) await sleep(PAUSE_MS);
      if (environment.signal?.aborted) throw new ReportError('REPORT_CANCELLED');
      const result = await search({ query: `from:me after:${after}`, count: 100, page, scope: 'messages', sort: 'timestamp' }) as {
        pages?: unknown; messages?: { matches?: unknown };
      };
      const matches = Array.isArray(result?.messages?.matches) ? result.messages.matches : [];
      for (const match of matches as { channel?: { name?: unknown; is_im?: unknown; is_mpim?: unknown } }[]) {
        const channel = match?.channel;
        if (!channel || channel.is_im === true || channel.is_mpim === true || typeof channel.name !== 'string') continue;
        const name = channel.name.slice(0, 80);
        counts.set(name, (counts.get(name) ?? 0) + 1);
      }
      if (typeof result?.pages !== 'number' || page >= result.pages) break;
    }
  } finally {
    await close();
  }
  const channels = [...counts].map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  return { ok: true, partial, since: after, channels };
}
