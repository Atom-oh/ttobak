'use strict';
const { CognitoJwtVerifier } = require('aws-jwt-verify');
const { SimpleJwksCache } = require('aws-jwt-verify/jwk');
const { SimpleFetcher } = require('aws-jwt-verify/https');

function checkConnectorToken(header, payload, config, now) {
  const { audience, clientIds } = config;
  const scopes = typeof payload.scope === 'string' ? payload.scope.split(' ') : [];
  const allowed = ['openid', 'email', 'profile', `${audience}/read`,
    ...(payload.client_id === clientIds[1] ? [`${audience}/write`] : [])];
  if (header.alg !== 'RS256' || payload.token_use !== 'access' || payload.aud !== audience ||
      !clientIds.includes(payload.client_id) || !Number.isSafeInteger(payload.exp) || payload.exp <= now ||
      typeof payload.sub !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(payload.sub) ||
      typeof payload.username !== 'string' || !payload.username ||
      !scopes.includes('openid') || !scopes.includes(`${audience}/read`) ||
      scopes.some(scope => !allowed.includes(scope))) throw new Error('INVALID_CONNECTOR_TOKEN');
}

function createConnectorAuthenticate({ userPoolId, clientIds, audience, browserAuthenticate,
  resolve, effectiveCapabilities, verifier, now = () => Math.floor(Date.now() / 1000) }) {
  const url = new URL(audience);
  if (!userPoolId || !Array.isArray(clientIds) || clientIds.length !== 2 || clientIds[0] === clientIds[1] ||
      clientIds.some(id => typeof id !== 'string' || !/^[A-Za-z0-9]{1,128}$/.test(id)) ||
      url.protocol !== 'https:' || url.pathname !== '/api/mcp' || url.search || url.hash ||
      url.username || url.password || url.href !== audience ||
      typeof browserAuthenticate !== 'function' || typeof resolve !== 'function' ||
      typeof effectiveCapabilities !== 'function') throw new Error('INVALID_CONNECTOR_CONFIG');
  const config = { audience, clientIds: Object.freeze([...clientIds]) };
  const v = verifier ?? CognitoJwtVerifier.create({
    userPoolId, clientId: [...clientIds], tokenUse: 'access', graceSeconds: 0,
    customJwtCheck: ({ header, payload }) => checkConnectorToken(header, payload, config, now()),
  }, { jwksCache: new SimpleJwksCache({
    fetcher: new SimpleFetcher({ defaultRequestOptions: { responseTimeout: 3000 } }),
  }) });
  return async (req, res, next) => {
    const match = /^Bearer ([^\s]+)$/i.exec(req.headers.authorization || '');
    if (!match) return browserAuthenticate(req, res, next);
    let payload;
    try {
      payload = await v.verify(match[1]);
      checkConnectorToken({ alg: 'RS256' }, payload, config, now());
    } catch { return browserAuthenticate(req, res, next); }
    const sub = payload.sub, clientId = payload.client_id, expires = payload.exp;
    const revalidate = async () => {
      if (now() >= expires) throw new Error('EXPIRED_CONNECTOR_TOKEN');
      const grants = await resolve(sub, clientId);
      effectiveCapabilities(sub, clientId, ['read'], 0, expires, grants, now());
      if (now() >= expires) throw new Error('EXPIRED_CONNECTOR_TOKEN');
    };
    try { await revalidate(); }
    catch { return res.status(403).json({ error: { code: 'CAPABILITY_DENIED', message: 'Connector access unavailable' } }); }
    req.sub = sub; req.user = '';
    req.revalidateConnector = revalidate;
    return next();
  };
}
module.exports = { createConnectorAuthenticate, checkConnectorToken };
