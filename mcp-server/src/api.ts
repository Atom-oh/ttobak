import { createReadStream, readFileSync, statSync, realpathSync } from 'node:fs';
import { request as httpsRequest } from 'node:https';
import { basename, extname, isAbsolute, sep } from 'node:path';
import { homedir } from 'node:os';
import { URL } from 'node:url';
import { MAX_READING_BYTES, type ReadingOptions } from './reading.js';

// Extension -> MIME type, shared by both upload tools for inference only --
// each tool advertises its own narrower format list (kb_upload: pdf/md/pptx/
// docx; upload_document: pdf/pptx/ppt, since documents feed the slide-preview
// pipeline while the KB ingests text formats too). Callers can still pass
// fileType explicitly to override (e.g. an unusual extension); this is only a
// convenience fallback, not validation -- the backend rejects unsupported
// types itself.
const MIME_BY_EXT: Record<string, string> = {
  '.pdf': 'application/pdf',
  '.pptx': 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  '.ppt': 'application/vnd.ms-powerpoint',
  '.md': 'text/markdown',
  '.docx': 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
};

export const MAX_UPLOAD_BYTES = 100 * 1024 * 1024; // 100MB (presigned document upload)
export const MAX_KB_UPLOAD_BYTES = 50 * 1024 * 1024; // 50MB -- Bedrock KB per-file ingestion limit
/** S3 answered a PUT with a failure status: the object was not stored. */
export class UploadRejectedError extends Error {}

export const MAX_AUDIO_UPLOAD_BYTES = 2 * 1024 * 1024 * 1024; // 2GiB -- matches the audio crop source cap

// Meeting audio formats for MCP uploads. Unlike documents this is an
// allowlist: the backend does not validate audio types. The production Whisper
// path decodes all of them through ffmpeg; .caf is not in the web picker, and
// the Transcribe fallback does not accept every listed container.
export const AUDIO_MIME_BY_EXT: Record<string, string> = {
  '.m4a': 'audio/mp4',
  '.mp4': 'audio/mp4',
  '.mp3': 'audio/mpeg',
  '.wav': 'audio/wav',
  '.webm': 'audio/webm',
  '.ogg': 'audio/ogg',
  '.flac': 'audio/flac',
  '.aac': 'audio/aac',
  '.caf': 'audio/x-caf',
};

// System paths and secret-shaped filenames a prompt-injected agent would
// reach for first. Checked against the symlink-resolved path/basename.
const BLOCKED_SYSTEM_PREFIXES = ['/etc', '/proc', '/sys', '/var/run/secrets', '/run/secrets'];
const BLOCKED_NAME_PATTERNS = [
  /^\.env(\..*)?$/i, // .env, .env.local, .env.production, ...
  /credentials/i, // credentials, aws_credentials.json, gcloud-credentials.txt, ...
  /\.(pem|key|p12|pfx)$/i, // TLS/private-key material
  /^id_[a-z0-9]+(\.pub)?$/i, // SSH keypairs: id_rsa, id_ed25519(.pub)
];

// Best-effort speed bump, NOT a sandbox. What it actually blocks: dotfile/
// dotdir paths under $HOME (~/.ttobak tokens, ~/.aws, ~/.ssh, ~/.gnupg, ...),
// system paths (/etc, /proc, /sys, /var/run/secrets), and secret-shaped
// filenames (.env*, *credentials*, *.pem, id_*) -- all resolved through
// realpathSync first so a symlink can't dodge the checks, since statSync/
// readFileSync follow symlinks even though path.resolve() doesn't -- plus
// non-regular files and anything over the size cap. A readable secret that
// matches none of these (an oddly-named token file in a project dir) still
// gets through: the real gate is the MCP host's tool-call approval; this
// only takes the most credential-dense targets out of one-click reach.
export function guardUploadPath(filePath: string, maxBytes: number): { path: string; size: number } {
  if (!isAbsolute(filePath)) {
    throw new Error(`filePath must be an absolute path, got "${filePath}".`);
  }
  let real: string;
  try {
    real = realpathSync(filePath);
  } catch {
    throw new Error(`File not found: "${filePath}".`);
  }
  for (const prefix of BLOCKED_SYSTEM_PREFIXES) {
    if (real === prefix || real.startsWith(prefix + sep)) {
      throw new Error(`Refusing to upload "${filePath}" -- path is inside the system directory ${prefix}.`);
    }
  }
  if (BLOCKED_NAME_PATTERNS.some((re) => re.test(basename(real)))) {
    throw new Error(`Refusing to upload "${filePath}" -- filename matches a credential/secret pattern.`);
  }
  const st = statSync(real);
  if (!st.isFile()) {
    throw new Error(`Refusing to upload "${filePath}" -- not a regular file.`);
  }
  const home = homedir();
  if (real === home || real.startsWith(home + sep)) {
    const relSegments = real === home ? [] : real.slice(home.length + 1).split(sep);
    if (relSegments.some((seg) => seg.startsWith('.'))) {
      throw new Error(`Refusing to upload "${filePath}" -- path is inside a hidden/credentials directory under $HOME.`);
    }
  }
  if (st.size > maxBytes) {
    throw new Error(`Refusing to upload "${filePath}" -- ${st.size} bytes exceeds the ${maxBytes}-byte limit.`);
  }
  return { path: real, size: st.size };
}

export function resolveFileMeta(filePath: string, fileName?: string, fileType?: string) {
  // basename() on the override too: a fileName containing path separators
  // must never reach the S3 key (the backend sanitizes as well -- this is
  // defense-in-depth at the client layer).
  const name = fileName ? basename(fileName) : basename(filePath);
  const type = fileType || MIME_BY_EXT[extname(name).toLowerCase()];
  if (!type) {
    throw new Error(
      `Could not infer a MIME type for "${name}" -- pass fileType explicitly (e.g. application/pdf).`,
    );
  }
  // Lowercase the effective extension for the upload key too, not just MIME
  // inference -- the convert-doc EventBridge rule matches `docs/*.pptx`/`.ppt`
  // (lowercase, case-sensitive), so `DECK.PPTX` would upload fine but never
  // get a PDF preview sidecar. An extensionless name (reachable with an
  // explicit fileType) passes through untouched -- slice(0, -0) would empty it.
  const ext = extname(name);
  const normalizedName = !ext || ext === ext.toLowerCase() ? name : name.slice(0, -ext.length) + ext.toLowerCase();
  return { name: normalizedName, type };
}

export const PROJECT_OPTIONAL_FIELDS = ['description', 'sfdcOpptyId', 'sfdcUrl', 'stage'] as const;

export type ProjectFields = {
  description?: string;
  sfdcOpptyId?: string;
  sfdcUrl?: string;
  stage?: string;
};

/** Fills any of `patch`'s optional fields left `undefined` with `current`'s
 * value, so a caller updating only `name` (or `stage`, etc.) doesn't wipe
 * the rest -- see updateProject's doc comment for why this is needed at all.
 * A field explicitly passed as '' clears it; only `undefined` inherits. */
export function mergeProjectUpdate(
  current: ProjectFields,
  patch: { name: string } & ProjectFields,
): { name: string } & ProjectFields {
  const merged: { name: string } & ProjectFields = { ...patch };
  for (const field of PROJECT_OPTIONAL_FIELDS) {
    if (merged[field] === undefined) {
      merged[field] = current[field] ?? '';
    }
  }
  return merged;
}

function identifier(value: string, name: string): string {
  if (typeof value !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(value)) {
    throw new Error(`${name} must be a non-empty ID (letters, digits, "_" or "-", at most 128 characters).`);
  }
  return encodeURIComponent(value);
}

function documentsPath(accountId?: string): string {
  return accountId === undefined
    ? '/api/documents'
    : `/api/accounts/${identifier(accountId, 'accountId')}/documents`;
}

export type DocumentInput = {
  title: string;
  markdown?: string;
  docType?: string;
  path?: string;
};

// HTTP success is mandatory even when the gateway returns JSON without the
// application's usual {error:{code,message}} envelope.
export function parseApiResponse(status: number, body: string): unknown {
  if (status === 204) return {};
  let parsed;
  try {
    parsed = JSON.parse(body);
  } catch {
    throw new Error(`HTTP ${status}: invalid JSON response`);
  }
  if (status < 200 || status >= 300 || parsed?.error) {
    const detail = parsed?.error;
    throw new Error(
      detail?.message
        ? `HTTP ${status} ${detail.code || 'API_ERROR'}: ${detail.message}`
        : `HTTP ${status}: TTOBAK request failed`,
    );
  }
  return parsed;
}

// The legacy name is retained for the stdio auth adapter. HTTP provides the
// verified, request-scoped OAuth access token through the same interface.
export interface ApiAuth {
  getIdToken(): Promise<string>;
}

export interface ApiRequestOptions {
  signal?: AbortSignal;
  timeoutMs?: number;
  maxResponseBytes?: number;
  allowLocalFiles?: boolean;
}

export class TtobakApi {
  constructor(
    private auth: ApiAuth,
    private baseUrl: string,
    private options: ApiRequestOptions = {},
  ) {}

  async listMeetings(opts?: { cursor?: string; limit?: number; tab?: string; accountIds?: string[] }) {
    const q = new URLSearchParams();
    if (opts?.cursor) q.set('cursor', opts.cursor);
    if (opts?.limit) q.set('limit', String(opts.limit));
    if (opts?.tab) q.set('tab', opts.tab);
    if (opts?.accountIds !== undefined) {
      if (!Array.isArray(opts.accountIds) || opts.accountIds.length === 0 || opts.accountIds.length > 100) {
        throw new Error('accountIds must contain 1-100 account IDs. Omit it to list all accounts.');
      }
      opts.accountIds.forEach((id) => identifier(id, 'accountId'));
      q.set('accountIds', [...new Set(opts.accountIds)].join(','));
    }
    const qs = q.toString();
    return this.get(`/api/meetings${qs ? '?' + qs : ''}`);
  }

  async readMeeting(options: ReadingOptions) {
    const query = new URLSearchParams({ kind: options.kind, pageSize: String(options.pageSize) });
    if (options.kind === 'meeting') query.set('section', options.section);
    else query.set('source', options.source);
    if (options.cursor !== undefined) query.set('cursor', options.cursor);
    if (options.kind === 'transcript' && options.startTime !== undefined) {
      query.set('startTime', String(options.startTime));
      query.set('endTime', String(options.endTime));
    }
    return this.request('GET', `/api/meetings/${identifier(options.meetingId, 'meetingId')}/reading?${query}`, undefined, MAX_READING_BYTES);
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

  async createProject(project: {
    name: string;
    description?: string;
    sfdcOpptyId?: string;
    sfdcUrl?: string;
    stage?: string;
  }) {
    return this.post('/api/projects', project);
  }

  async listProjects() {
    return this.get('/api/projects');
  }

  async getProject(projectId: string) {
    return this.get(`/api/projects/${encodeURIComponent(projectId)}`);
  }

  async getProjectBrief(
    projectId: string,
    opts?: { from?: string; to?: string; types?: string[] },
  ) {
    const q = new URLSearchParams();
    if (opts?.from) q.set('from', opts.from);
    if (opts?.to) q.set('to', opts.to);
    if (opts?.types && opts.types.length) q.set('types', opts.types.join(','));
    const qs = q.toString();
    return this.get(`/api/projects/${encodeURIComponent(projectId)}/brief${qs ? '?' + qs : ''}`);
  }

  async getProjectInsights(
    projectId: string,
    opts?: { from?: string; to?: string; types?: string[] },
  ) {
    const q = new URLSearchParams();
    if (opts?.from) q.set('from', opts.from);
    if (opts?.to) q.set('to', opts.to);
    if (opts?.types && opts.types.length) q.set('types', opts.types.join(','));
    const qs = q.toString();
    return this.get(`/api/projects/${encodeURIComponent(projectId)}/insights${qs ? '?' + qs : ''}`);
  }

  /** `name` is required even when only changing e.g. stage -- the backend's
   * UpdateProjectRequest has no partial/omit-preserves semantics of its own
   * (plain string fields, unlike Meeting's *string pointer fields), so an
   * omitted optional field would decode to "" server-side and silently wipe
   * the current value. mergeProjectUpdate fills any field the caller left
   * undefined from the project's current value before we PUT. */
  async updateProject(projectId: string, project: {
    name: string;
    description?: string;
    sfdcOpptyId?: string;
    sfdcUrl?: string;
    stage?: string;
  }) {
    const needsMerge = PROJECT_OPTIONAL_FIELDS.some((f) => project[f] === undefined);
    const current = needsMerge ? (await this.getProject(projectId)) as ProjectFields : undefined;
    const body = current ? mergeProjectUpdate(current, project) : project;
    return this.put(`/api/projects/${encodeURIComponent(projectId)}`, body);
  }

  async linkProjectAccount(projectId: string, accountId: string) {
    return this.post(`/api/projects/${encodeURIComponent(projectId)}/accounts`, { accountId });
  }

  async unlinkProjectAccount(projectId: string, accountId: string) {
    return this.delete(`/api/projects/${encodeURIComponent(projectId)}/accounts/${encodeURIComponent(accountId)}`);
  }

  async exportVault() {
    return this.get('/api/vault/export');
  }

  async putDocument(
    accountId: string | undefined,
    doc: { title: string; markdown: string; docType?: string; path?: string },
  ) {
    return this.post(documentsPath(accountId), doc);
  }

  async listDocuments(accountId?: string, docType?: string) {
    const q = new URLSearchParams();
    if (docType) q.set('docType', docType);
    const qs = q.toString();
    return this.get(`${documentsPath(accountId)}${qs ? '?' + qs : ''}`);
  }

  async getDocument(accountId: string | undefined, docId: string) {
    return this.get(`${documentsPath(accountId)}/${identifier(docId, 'docId')}`);
  }

  async updateDocument(accountId: string | undefined, docId: string, doc: DocumentInput) {
    return this.put(`${documentsPath(accountId)}/${identifier(docId, 'docId')}`, doc);
  }

  /** Upload a local file into the global Knowledge Base. Ingestion doesn't
   * start until syncKB() is called (upload can be batched, then synced once). */
  async uploadToKB(filePath: string, fileName?: string, fileType?: string) {
    this.requireLocalFiles();
    const { path: resolvedPath } = guardUploadPath(filePath, MAX_KB_UPLOAD_BYTES);
    const { name, type } = resolveFileMeta(resolvedPath, fileName, fileType);
    const { uploadUrl, key } = (await this.post('/api/kb/upload', {
      fileName: name,
      fileType: type,
    })) as { uploadUrl: string; key: string };
    await this.putFile(uploadUrl, resolvedPath, type);
    return { key, fileName: name, mimeType: type };
  }

  async uploadBytesToKB(data: Buffer, fileName: string, fileType?: string) {
    const { name, type } = resolveFileMeta(fileName, fileName, fileType);
    const { uploadUrl, key } = (await this.post('/api/kb/upload', {
      fileName: name, fileType: type,
    })) as { uploadUrl: string; key: string };
    await this.putBytes(uploadUrl, data, type);
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
    this.requireLocalFiles();
    const { path: resolvedPath, size: fileSize } = guardUploadPath(filePath, MAX_UPLOAD_BYTES);
    const { name, type } = resolveFileMeta(resolvedPath, opts?.fileName, opts?.fileType);
    const { uploadUrl, key } = (await this.post('/api/upload/presigned', {
      fileName: name,
      fileType: type,
      category: 'doc',
    })) as { uploadUrl: string; key: string };
    await this.putFile(uploadUrl, resolvedPath, type);
    const target = opts?.accountId
      ? `/api/accounts/${encodeURIComponent(opts.accountId)}/documents`
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

  async uploadDocumentBytes(
    data: Buffer, title: string, fileName: string,
    opts?: { accountId?: string; fileType?: string; docType?: string; path?: string },
  ) {
    const target = documentsPath(opts?.accountId);
    const { name, type } = resolveFileMeta(fileName, fileName, opts?.fileType);
    const { uploadUrl, key } = (await this.post('/api/upload/presigned', {
      fileName: name, fileType: type, category: 'doc',
    })) as { uploadUrl: string; key: string };
    await this.putBytes(uploadUrl, data, type);
    return this.post(target, {
      title, fileKey: key, fileName: name, mimeType: type, fileSize: data.length,
      docType: opts?.docType, path: opts?.path,
    });
  }

  async createMeeting(input: { title: string; date?: string; participants?: string[]; accountId?: string }) {
    const result = (await this.post('/api/meetings', input)) as { meetingId?: unknown };
    if (typeof result?.meetingId !== 'string' || !result.meetingId) throw new Error('Meeting creation returned no meetingId');
    return result as { meetingId: string } & Record<string, unknown>;
  }

  /** Validates a local meeting audio file before any meeting is created for it. */
  resolveMeetingAudio(filePath: string): { path: string; size: number; ext: string; type: string } {
    this.requireLocalFiles();
    const { path, size } = guardUploadPath(filePath, MAX_AUDIO_UPLOAD_BYTES);
    const ext = extname(path).toLowerCase();
    const type = AUDIO_MIME_BY_EXT[ext];
    if (!type) throw new Error(`Unsupported audio format "${ext || 'none'}"; use ${Object.keys(AUDIO_MIME_BY_EXT).join(', ')}`);
    if (!size) throw new Error('Refusing to upload an empty audio file');
    return { path, size, ext, type };
  }

  /** Issues an S3 upload for meeting audio. The PUT of the returned URL is
   * what starts transcription (S3 event under audio/). A fixed object name
   * keeps user file names containing the transcribe Lambda's skip markers
   * (checkpoint_, recording_progress, realtime_, part_NNN_) out of the key. */
  async presignMeetingAudio(meetingId: string, ext: string, type: string): Promise<{ uploadUrl: string; key: string }> {
    const result = (await this.post('/api/upload/presigned', {
      fileName: `mcp_upload_${Date.now()}${ext}`, fileType: type, category: 'audio', meetingId,
    })) as { uploadUrl?: unknown; key?: unknown };
    if (typeof result?.uploadUrl !== 'string' || typeof result.key !== 'string') throw new Error('Presign returned no upload URL');
    return { uploadUrl: result.uploadUrl, key: result.key };
  }

  /** Streams the file to the presigned URL. Throws UploadRejectedError only when
   * S3 answered with a failure status (nothing stored); any other error leaves
   * the outcome unknown. */
  async putMeetingAudio(uploadUrl: string, filePath: string, size: number, type: string): Promise<void> {
    await this.putFileStream(uploadUrl, filePath, size, type);
  }

  /** Binds uploaded audio to the meeting and marks it transcribing. */
  async completeMeetingAudio(meetingId: string, key: string, size: number, type: string): Promise<void> {
    await this.post('/api/upload/complete', { meetingId, key, category: 'audio', fileSize: size, mimeType: type });
  }

  meetingUrl(meetingId: string): string {
    return new URL(`/meeting/${encodeURIComponent(meetingId)}`, this.baseUrl).toString();
  }

  private requireLocalFiles(): void {
    if (this.options.allowLocalFiles === false) throw new Error('Local file access is unavailable over HTTP MCP');
  }

  async createAccount(input: {
    name: string;
    aliases?: string[];
    domains?: string[];
    industry?: string;
    parentAccountId?: string;
  }) {
    return this.post('/api/accounts', input);
  }

  async addAccountMember(accountId: string, email: string, role: string) {
    return this.post(`/api/accounts/${encodeURIComponent(accountId)}/members`, { email, role });
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

  private async put(path: string, body: unknown) {
    return this.request('PUT', path, body);
  }

  /** PUT a local file's bytes directly to a presigned S3 URL -- no TTOBAK
   * bearer token here, the URL's own signature is the auth. */
  private async putFile(uploadUrl: string, filePath: string, contentType: string): Promise<void> {
    this.requireLocalFiles();
    const data = readFileSync(filePath);
    return this.putBytes(uploadUrl, data, contentType);
  }

  /** Streams a large local file to a presigned S3 URL without buffering it.
   * The request `timeout` is Node's socket idle timeout, so it aborts a
   * stalled upload rather than capping total duration (large recordings on
   * slow links must be allowed to finish). */
  private async putFileStream(uploadUrl: string, filePath: string, size: number, contentType: string): Promise<void> {
    this.requireLocalFiles();
    const url = new URL(uploadUrl);
    if (url.protocol !== 'https:' || url.username || url.password) throw new Error('Invalid upload URL');
    return new Promise((resolve, reject) => {
      const req = httpsRequest(
        {
          hostname: url.hostname,
          port: url.port || undefined,
          path: url.pathname + url.search,
          method: 'PUT',
          headers: { 'Content-Type': contentType, 'Content-Length': String(size) },
          timeout: this.options.timeoutMs ?? 60_000,
          signal: this.options.signal,
        },
        (res) => {
          res.resume();
          res.on('end', () => {
            if (res.statusCode && res.statusCode >= 200 && res.statusCode < 300) resolve();
            else reject(new UploadRejectedError(`Audio upload to S3 failed: HTTP ${res.statusCode}`));
          });
          res.on('error', reject);
          res.on('aborted', () => reject(new Error('Upload response aborted')));
        },
      );
      const source = createReadStream(filePath);
      source.on('error', (err) => req.destroy(err));
      req.on('timeout', () => req.destroy(new Error('Audio upload to S3 stalled')));
      req.on('error', (err) => { source.destroy(); reject(err); });
      source.pipe(req);
    });
  }

  private async putBytes(uploadUrl: string, data: Buffer, contentType: string): Promise<void> {
    const url = new URL(uploadUrl);
    if (url.protocol !== 'https:' || url.username || url.password) throw new Error('Invalid upload URL');
    return new Promise((resolve, reject) => {
      const req = httpsRequest(
        {
          hostname: url.hostname,
          port: url.port || undefined,
          path: url.pathname + url.search,
          method: 'PUT',
          headers: { 'Content-Type': contentType, 'Content-Length': String(data.length) },
          timeout: this.options.timeoutMs ?? 60_000,
          signal: this.options.signal,
        },
        (res) => {
          // Upload responses never contribute data to the next request.
          res.resume();
          res.on('end', () => {
            if (res.statusCode && res.statusCode >= 200 && res.statusCode < 300) {
              resolve();
            } else {
              reject(new Error(`File upload to S3 failed: HTTP ${res.statusCode}`));
            }
          });
          res.on('error', reject);
          res.on('aborted', () => reject(new Error('Upload response aborted')));
        },
      );
      req.on('timeout', () => req.destroy(new Error('File upload to S3 timed out')));
      req.on('error', reject);
      req.write(data);
      req.end();
    });
  }

  private async request(method: string, path: string, body?: unknown, maxResponseBytes?: number): Promise<unknown> {
    this.options.signal?.throwIfAborted();
    const idToken = await this.auth.getIdToken();
    const url = new URL(path, this.baseUrl);
    // A tool argument must never replace the configured application origin.
    if (url.origin !== new URL(this.baseUrl).origin) throw new Error('API origin cannot change');
    maxResponseBytes ??= this.options.maxResponseBytes;
    const data = body ? JSON.stringify(body) : undefined;

    return new Promise((resolve, reject) => {
      const req = httpsRequest(
        {
          hostname: url.hostname,
          port: url.port || undefined,
          path: url.pathname + url.search,
          method,
          timeout: this.options.timeoutMs ?? 120_000,
          signal: this.options.signal,
          headers: {
            Authorization: `Bearer ${idToken}`,
            'Content-Type': 'application/json',
            ...(data ? { 'Content-Length': String(Buffer.byteLength(data)) } : {}),
          },
        },
        (res) => {
          // Count original wire bytes before buffering/decoding JSON. A string
          // character count would undercount Korean/emoji and byte-split chunks.
          const chunks: Buffer[] = [];
          let received = 0, finished = false;
          const fail = (error: Error) => {
            if (finished) return;
            finished = true;
            chunks.length = 0;
            reject(error);
            res.destroy();
            req.destroy();
          };
          const tooLarge = () => fail(new Error(maxResponseBytes === MAX_READING_BYTES
            ? 'READING_LIMIT: HTTP reading response exceeds 32000 bytes'
            : `RESULT_TOO_LARGE: HTTP API response exceeds ${maxResponseBytes} bytes`));
          res.on('error', fail);
          res.on('aborted', () => fail(new Error('TTOBAK response was aborted')));
          res.on('close', () => { if (!finished) fail(new Error('TTOBAK response ended before completion')); });
          res.on('data', (chunk: Buffer) => {
            if (finished) return;
            if (maxResponseBytes !== undefined && received + chunk.length > maxResponseBytes) {
              tooLarge();
              return;
            }
            received += chunk.length;
            chunks.push(chunk);
          });
          res.on('end', () => {
            if (finished) return;
            finished = true;
            try {
              resolve(parseApiResponse(res.statusCode || 0, Buffer.concat(chunks, received).toString('utf8')));
            } catch (error) {
              reject(error);
            }
          });
          const declaredLength = res.headers?.['content-length'];
          if (maxResponseBytes !== undefined && declaredLength !== undefined && Number(declaredLength) > maxResponseBytes) {
            tooLarge();
          }
        },
      );
      req.on('timeout', () => req.destroy(new Error('TTOBAK request timed out')));
      req.on('error', reject);
      if (data) req.write(data);
      req.end();
    });
  }
}
