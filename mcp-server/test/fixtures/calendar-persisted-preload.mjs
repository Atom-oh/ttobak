import fs from 'node:fs';
import os from 'node:os';
import https from 'node:https';
import http from 'node:http';
import { syncBuiltinESMExports } from 'node:module';
import { EventEmitter } from 'node:events';
import { Readable } from 'node:stream';
import { authJson, saveCache, tokens } from './local-auth-data.mjs';

const endpoint = new URL(process.env.CALENDAR_PERSISTED_URL);
if (endpoint.protocol !== 'http:' || endpoint.hostname !== '127.0.0.1') throw new Error('Persisted fixture requires loopback');
const fixtureHome = fs.mkdtempSync('/tmp/ttobak-calendar-persisted-');
os.homedir = () => fixtureHome;
const credentials = tokens();
saveCache(fixtureHome + '/.ttobak/tokens', credentials);
process.once('exit', () => fs.rmSync(fixtureHome, { recursive: true, force: true }));
process.once('SIGTERM', () => process.exit(0));
let drop = process.env.CALENDAR_DROP_RESPONSE === '1';
https.request = (options, callbackOrOptions, authCallback) => {
  if (options instanceof URL) {
    const request = new EventEmitter();
    request.setTimeout = request.destroy = () => request;
    request.end = () => void authJson(options).then(value => {
      const response = Readable.from([Buffer.from(JSON.stringify(value))]);
      response.statusCode = 200;
      response.headers = {};
      authCallback(response);
    });
    return request;
  }
  if (options.hostname !== 'example.invalid' || options.headers.Authorization !== 'Bearer ' + credentials.id_token ||
      !/^\/api\/calendar\/(?:events(?:\?|$)|sources(?:\?|$|\/[a-f0-9-]+\/sync$))/.test(options.path)) {
    throw new Error('Unexpected persisted calendar origin, route or authentication');
  }
  const request = http.request({
    hostname: endpoint.hostname, port: endpoint.port, path: options.path, method: options.method,
    headers: { ...options.headers, Authorization: 'Bearer ' + process.env.CALENDAR_CALLER_TOKEN },
  }, response => {
    if (drop && options.method === 'POST' && response.statusCode === 200) {
      drop = false;
      response.resume();
      response.once('end', () => request.emit('error', new Error('Synthetic response lost after persisted commit')));
      return;
    }
    callbackOrOptions(response);
  });
  return request;
};
syncBuiltinESMExports();
