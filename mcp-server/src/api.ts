import { request as httpsRequest } from 'node:https';
import { URL } from 'node:url';
import type { CognitoAuth } from './auth.js';

export class TtobakApi {
  constructor(
    private auth: CognitoAuth,
    private baseUrl: string,
  ) {}

  async listMeetings(opts?: { cursor?: string; limit?: number; tab?: string }) {
    const q = new URLSearchParams();
    if (opts?.cursor) q.set('cursor', opts.cursor);
    if (opts?.limit) q.set('limit', String(opts.limit));
    if (opts?.tab) q.set('tab', opts.tab);
    const qs = q.toString();
    return this.get(`/api/meetings${qs ? '?' + qs : ''}`);
  }

  async getMeeting(meetingId: string) {
    return this.get(`/api/meetings/${meetingId}`);
  }

  async askQuestion(question: string, meetingId?: string, sessionId?: string) {
    const body: Record<string, string> = { question };
    if (sessionId) body.sessionId = sessionId;

    if (meetingId) {
      return this.post(`/api/qa/meeting/${meetingId}`, body);
    }
    return this.post('/api/qa/ask', body);
  }

  private async get(path: string) {
    return this.request('GET', path);
  }

  private async post(path: string, body: unknown) {
    return this.request('POST', path, body);
  }

  private async request(method: string, path: string, body?: unknown): Promise<unknown> {
    const idToken = await this.auth.getIdToken();
    const url = new URL(path, this.baseUrl);
    const data = body ? JSON.stringify(body) : undefined;

    return new Promise((resolve, reject) => {
      const req = httpsRequest(
        {
          hostname: url.hostname,
          path: url.pathname + url.search,
          method,
          headers: {
            Authorization: `Bearer ${idToken}`,
            'Content-Type': 'application/json',
            ...(data ? { 'Content-Length': String(Buffer.byteLength(data)) } : {}),
          },
        },
        (res) => {
          let chunks = '';
          res.on('data', (c) => (chunks += c));
          res.on('end', () => {
            if (res.statusCode === 204) return resolve({});
            try {
              const parsed = JSON.parse(chunks);
              if (parsed.error) {
                reject(new Error(`${parsed.error.code}: ${parsed.error.message}`));
              } else {
                resolve(parsed);
              }
            } catch {
              reject(new Error(`HTTP ${res.statusCode}: ${chunks.slice(0, 300)}`));
            }
          });
        },
      );
      req.on('error', reject);
      if (data) req.write(data);
      req.end();
    });
  }
}
