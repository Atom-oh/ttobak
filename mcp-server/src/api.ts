import { readFileSync, statSync } from 'node:fs';
import { request as httpsRequest } from 'node:https';
import { basename, extname } from 'node:path';
import { URL } from 'node:url';
import type { CognitoAuth } from './auth.js';

// Extension -> MIME type, for the handful of formats KB/document upload
// actually accept server-side. Callers can still pass fileType explicitly
// to override (e.g. an unusual extension); this is only a convenience
// fallback, not validation -- the backend rejects unsupported types itself.
const MIME_BY_EXT: Record<string, string> = {
  '.pdf': 'application/pdf',
  '.pptx': 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  '.ppt': 'application/vnd.ms-powerpoint',
  '.md': 'text/markdown',
  '.docx': 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
};

function resolveFileMeta(filePath: string, fileName?: string, fileType?: string) {
  const name = fileName || basename(filePath);
  const type = fileType || MIME_BY_EXT[extname(filePath).toLowerCase()];
  if (!type) {
    throw new Error(
      `Could not infer a MIME type for "${name}" -- pass fileType explicitly (e.g. application/pdf).`,
    );
  }
  return { name, type };
}

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

  async listAccounts() {
    return this.get('/api/accounts');
  }

  async getAccount(accountId: string) {
    return this.get(`/api/accounts/${accountId}`);
  }

  async getAccountMeetings(accountId: string) {
    return this.get(`/api/accounts/${accountId}/meetings`);
  }

  async getAccountInsights(
    accountId: string,
    opts?: { from?: string; to?: string; types?: string[] },
  ) {
    const q = new URLSearchParams();
    if (opts?.from) q.set('from', opts.from);
    if (opts?.to) q.set('to', opts.to);
    if (opts?.types && opts.types.length) q.set('types', opts.types.join(','));
    const qs = q.toString();
    return this.get(`/api/accounts/${accountId}/insights${qs ? '?' + qs : ''}`);
  }

  async getAccountBrief(
    accountId: string,
    opts?: { from?: string; to?: string; types?: string[] },
  ) {
    const q = new URLSearchParams();
    if (opts?.from) q.set('from', opts.from);
    if (opts?.to) q.set('to', opts.to);
    if (opts?.types && opts.types.length) q.set('types', opts.types.join(','));
    const qs = q.toString();
    return this.get(`/api/accounts/${accountId}/brief${qs ? '?' + qs : ''}`);
  }

  async exportVault() {
    return this.get('/api/vault/export');
  }

  async putDocument(
    accountId: string,
    doc: { title: string; markdown: string; docType?: string; path?: string },
  ) {
    return this.post(`/api/accounts/${accountId}/documents`, doc);
  }

  async listDocuments(accountId: string, docType?: string) {
    const q = new URLSearchParams();
    if (docType) q.set('docType', docType);
    const qs = q.toString();
    return this.get(`/api/accounts/${accountId}/documents${qs ? '?' + qs : ''}`);
  }

  async getDocument(accountId: string, docId: string) {
    return this.get(`/api/accounts/${accountId}/documents/${docId}`);
  }

  /** Upload a local file into the global Knowledge Base. Ingestion doesn't
   * start until syncKB() is called (upload can be batched, then synced once). */
  async uploadToKB(filePath: string, fileName?: string, fileType?: string) {
    const { name, type } = resolveFileMeta(filePath, fileName, fileType);
    const { uploadUrl, key } = (await this.post('/api/kb/upload', {
      fileName: name,
      fileType: type,
    })) as { uploadUrl: string; key: string };
    await this.putFile(uploadUrl, filePath, type);
    return { key, fileName: name, mimeType: type };
  }

  async syncKB() {
    return this.post('/api/kb/sync', {});
  }

  async listKBFiles() {
    return this.get('/api/kb/files');
  }

  async deleteKBFile(fileId: string) {
    return this.delete(`/api/kb/files/${encodeURIComponent(fileId)}`);
  }

  /** Upload a local file and register it as a document -- either under an
   * account (accountId set) or as a personal doc (accountId omitted). */
  async uploadDocument(
    filePath: string,
    title: string,
    opts?: {
      accountId?: string;
      fileName?: string;
      fileType?: string;
      docType?: string;
      path?: string;
    },
  ) {
    const { name, type } = resolveFileMeta(filePath, opts?.fileName, opts?.fileType);
    const { uploadUrl, key } = (await this.post('/api/upload/presigned', {
      fileName: name,
      fileType: type,
      category: 'doc',
    })) as { uploadUrl: string; key: string };
    await this.putFile(uploadUrl, filePath, type);
    const fileSize = statSync(filePath).size;
    const target = opts?.accountId
      ? `/api/accounts/${opts.accountId}/documents`
      : '/api/documents';
    return this.post(target, {
      title,
      fileKey: key,
      fileName: name,
      mimeType: type,
      fileSize,
      docType: opts?.docType,
      path: opts?.path,
    });
  }

  async createAccount(input: {
    name: string;
    aliases?: string[];
    domains?: string[];
    industry?: string;
  }) {
    return this.post('/api/accounts', input);
  }

  async addAccountMember(accountId: string, email: string, role: string) {
    return this.post(`/api/accounts/${accountId}/members`, { email, role });
  }

  private async get(path: string) {
    return this.request('GET', path);
  }

  private async post(path: string, body: unknown) {
    return this.request('POST', path, body);
  }

  private async delete(path: string) {
    return this.request('DELETE', path);
  }

  /** PUT a local file's bytes directly to a presigned S3 URL -- no TTOBAK
   * bearer token here, the URL's own signature is the auth. */
  private async putFile(uploadUrl: string, filePath: string, contentType: string): Promise<void> {
    const data = readFileSync(filePath);
    const url = new URL(uploadUrl);
    return new Promise((resolve, reject) => {
      const req = httpsRequest(
        {
          hostname: url.hostname,
          path: url.pathname + url.search,
          method: 'PUT',
          headers: { 'Content-Type': contentType, 'Content-Length': String(data.length) },
        },
        (res) => {
          res.on('data', () => {});
          res.on('end', () => {
            if (res.statusCode && res.statusCode >= 200 && res.statusCode < 300) {
              resolve();
            } else {
              reject(new Error(`File upload to S3 failed: HTTP ${res.statusCode}`));
            }
          });
        },
      );
      req.on('error', reject);
      req.write(data);
      req.end();
    });
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
