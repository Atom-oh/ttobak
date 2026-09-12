// Exercise real API/auth/stdio code in compiled and bundled modes. Only native
// credentials and HTTPS are replaced; no sockets or real credentials are used.
import fs from 'node:fs';
import os from 'node:os';
import https from 'node:https';
import { syncBuiltinESMExports } from 'node:module';
import { EventEmitter } from 'node:events';
import { Readable } from 'node:stream';
import { CURSOR, responseFixture } from './reading-data.mjs';

os.homedir = () => '/synthetic-ttobak-reading';
const tokenFile = '/synthetic-ttobak-reading/.ttobak/tokens.json';
const exists = fs.existsSync, read = fs.readFileSync;
fs.existsSync = (path) => path === tokenFile || exists(path);
fs.readFileSync = (path, ...args) => path === tokenFile
  ? JSON.stringify({ id_token: 'synthetic-token', expires_at: Date.now() / 1000 + 3600 })
  : read(path, ...args);

const calls = new Map();
const trace = (event) => process.stderr.write(`READING_FIXTURE ${JSON.stringify(event)}\n`);
https.request = (options, onResponse) => {
  trace({ event: 'request', path: options.path, method: options.method });
  if (options.hostname !== 'example.invalid' || options.method !== 'GET' ||
      options.headers.Authorization !== 'Bearer synthetic-token' ||
      !/^\/api\/meetings\/[A-Za-z0-9_-]+\/reading\?/.test(options.path)) {
    throw new Error('Full meeting requests and unauthenticated requests are forbidden');
  }
  const url = new URL(options.path, 'https://example.invalid');
  const id = url.pathname.split('/')[3];
  const call = (calls.get(id) ?? 0) + 1;
  calls.set(id, call);
  const query = url.searchParams;
  const request = new EventEmitter();
  request.write = () => { throw new Error('Unexpected request body'); };
  request.destroy = () => { request.destroyed = true; return request; };
  request.end = () => queueMicrotask(() => {
    const denied = /^deny(401|403|404)$/.exec(id);
    let status = denied && call > 1 ? Number(denied[1]) : 200;
    let errorCode = 'DENIED';
    if (id === 'missing-route') status = 404;
    if (id === 'server-error') status = 500;
    if (id === 'stale' || (query.has('cursor') && query.get('cursor') !== CURSOR)) {
      status = 409; errorCode = 'STALE_CURSOR';
    }
    if (id === 'invalid-range') { status = 400; errorCode = 'TIME_RANGE_UNAVAILABLE'; }
    const value = status === 200 ? responseFixture(id, query) : {
      error: { code: errorCode, message: `${errorCode}: synthetic server rejection` },
    };
    const bytes = Buffer.from(id === 'non-json' ? '<html>gateway failure</html>' : JSON.stringify(value));
    let sent = 0;
    const response = new Readable({
      highWaterMark: 512,
      read() {
        if (id === 'interrupted' && sent > 0) { this.destroy(new Error('synthetic interrupted response')); return; }
        if (sent === bytes.length) { this.push(null); return; }
        // Deliberately split Korean and emoji UTF-8 sequences across chunks.
        const end = Math.min(sent + 113, bytes.length);
        const chunk = bytes.subarray(sent, end);
        sent = end;
        this.push(chunk);
      },
    });
    response.statusCode = status;
    response.headers = id === 'oversized-header' ? { 'content-length': '32001' } : {};
    response.on('close', () => trace({
      event: 'response-closed', id, sent, total: bytes.length,
      requestDestroyed: !!request.destroyed, responseDestroyed: response.destroyed,
    }));
    onResponse(response);
  });
  return request;
};
syncBuiltinESMExports();
