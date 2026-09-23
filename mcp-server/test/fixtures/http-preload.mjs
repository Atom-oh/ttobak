// Inject only a synthetic public JWKS into the standalone HTTP process.
// No AWS request or local credential read is permitted by this fixture.
import https from 'node:https';
import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
import { EventEmitter } from 'node:events';
import { Readable } from 'node:stream';

const readFile = fs.readFileSync;
fs.readFileSync = function(path, ...args) {
  if (String(path).includes('.ttobak/tokens.json')) throw new Error('HTTP attempted to read local credentials');
  return readFile.call(this, path, ...args);
};
https.request = function(url, options, callback) {
  if (typeof options === 'function') { callback = options; options = {}; }
  const target = typeof url === 'string' || url instanceof URL
    ? new URL(url) : new URL(`https://${url.hostname}${url.path}`);
  if (target.hostname !== 'cognito-idp.ap-northeast-2.amazonaws.com' ||
      target.pathname !== '/ap-northeast-2_testpool/.well-known/jwks.json') {
    throw new Error('Unexpected external request in HTTP bundle fixture');
  }
  const req = new EventEmitter();
  req.destroy = error => { if (error) req.emit('error', error); };
  req.end = () => queueMicrotask(() => {
    const response = new Readable({ read() {} });
    response.statusCode = 200;
    response.headers = { 'content-type': 'application/json' };
    callback(response);
    response.push(process.env.TTOBAK_TEST_JWKS);
    response.push(null);
  });
  return req;
};
syncBuiltinESMExports();
