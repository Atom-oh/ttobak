import { createConnection } from 'node:net';
import { randomUUID } from 'node:crypto';
import { homedir } from 'node:os';
import { join } from 'node:path';

// Client for the TTOBAK Mac app's local control socket (ADR-046). The app
// records system audio mixed with the microphone and owns sign-in, meeting
// creation and upload; this adapter only asks it to start/stop and reads
// status. Stdio only: the HTTP transport never exposes these tools.

export const MAX_APP_RESPONSE_BYTES = 64 * 1024;
const DEFAULT_TIMEOUT_MS = 60_000;

export function defaultAppSocketPath(): string {
  return process.env.TTOBAK_APP_SOCKET || join(homedir(), 'Library', 'Application Support', 'ttobak', 'control.sock');
}

export type AppAction =
  | { action: 'status' }
  | { action: 'start'; title: string; accountId?: string }
  | { action: 'stop'; upload?: boolean };

export interface AppReply {
  ok: boolean;
  data?: Record<string, unknown>;
  error?: { code: string; message: string };
}

export class AppControlError extends Error {
  constructor(readonly code: string, message: string) {
    super(message);
  }
}

/** Sends one request line and resolves with the app's parsed reply. */
export function sendAppRequest(
  request: AppAction,
  options: { socketPath?: string; timeoutMs?: number } = {},
): Promise<AppReply> {
  const socketPath = options.socketPath ?? defaultAppSocketPath();
  const id = randomUUID();
  const line = JSON.stringify({ v: 1, id, ...request }) + '\n';
  return new Promise((resolve, reject) => {
    const socket = createConnection(socketPath);
    const chunks: Buffer[] = [];
    let size = 0;
    let settled = false;
    const settle = (fn: () => void) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      socket.destroy();
      fn();
    };
    const timer = setTimeout(() => settle(() => reject(new AppControlError('timeout',
      'The TTOBAK Mac app did not answer in time. Its state is unknown: check ttobak_app_status before retrying.'))),
    options.timeoutMs ?? DEFAULT_TIMEOUT_MS);

    socket.on('connect', () => socket.write(line));
    socket.on('data', (chunk: Buffer) => {
      size += chunk.length;
      if (size > MAX_APP_RESPONSE_BYTES) {
        settle(() => reject(new AppControlError('bad_response', 'The TTOBAK Mac app sent an oversized response.')));
        return;
      }
      chunks.push(chunk);
      if (chunk.includes(0x0a)) settle(() => finish());
    });
    socket.on('end', () => settle(() => finish()));
    socket.on('error', (err: NodeJS.ErrnoException) => settle(() => reject(
      err.code === 'ENOENT' || err.code === 'ECONNREFUSED'
        ? new AppControlError('app_not_running',
          'The TTOBAK Mac app is not running (or is an older build without MCP control). Open it, or use ttobak_start_recording for microphone-only recording.')
        : new AppControlError('connection_failed', `Could not reach the TTOBAK Mac app: ${err.code ?? err.message}`),
    )));

    function finish() {
      const text = Buffer.concat(chunks).toString('utf8');
      const first = text.split('\n', 1)[0];
      let parsed: unknown;
      try {
        parsed = JSON.parse(first);
      } catch {
        reject(new AppControlError('bad_response', 'The TTOBAK Mac app sent an unreadable response.'));
        return;
      }
      const reply = parsed as Record<string, unknown>;
      if (!reply || typeof reply !== 'object' || reply.v !== 1 || typeof reply.ok !== 'boolean' ||
          (reply.id !== id && reply.id !== null)) {
        reject(new AppControlError('bad_response', 'The TTOBAK Mac app sent an unexpected response.'));
        return;
      }
      const error = reply.error as { code?: unknown; message?: unknown } | undefined;
      resolve({
        ok: reply.ok,
        data: reply.data && typeof reply.data === 'object' ? reply.data as Record<string, unknown> : undefined,
        error: reply.ok ? undefined : {
          code: typeof error?.code === 'string' ? error.code : 'unknown',
          message: typeof error?.message === 'string' ? error.message : 'The app reported an error.',
        },
      });
    }
  });
}
