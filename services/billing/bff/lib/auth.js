'use strict';
const { CognitoJwtVerifier } = require('aws-jwt-verify');

const BEARER = /^Bearer ([^\s]+)$/i;

function unauthorized(res) {
  return res.status(401).json({
    error: { code: 'UNAUTHORIZED', message: 'Authentication required' },
  });
}

// Identity comes only from `Authorization: Bearer <JWT>`. Cookies, query
// strings and other headers are never consulted. Neither tokens nor verifier
// error details are logged or returned.
function createAuthenticate({ userPoolId, clientIds, verifier }) {
  if (!userPoolId) throw new Error('userPoolId is required');
  if (!Array.isArray(clientIds) || clientIds.length === 0) {
    throw new Error('clientIds must be a non-empty list (fail closed)');
  }
  const v = verifier || CognitoJwtVerifier.create({
    userPoolId,
    tokenUse: null, // ID or access token; aud / client_id checked against clientIds
    clientId: clientIds,
  });

  return async function authenticate(req, res, next) {
    const match = BEARER.exec(req.headers.authorization || '');
    if (!match) return unauthorized(res);
    let payload;
    try {
      payload = await v.verify(match[1]);
    } catch {
      return unauthorized(res);
    }
    if (!payload || typeof payload.sub !== 'string' || !payload.sub) {
      return unauthorized(res);
    }
    req.sub = payload.sub;
    req.user = typeof payload.email === 'string' ? payload.email : '';
    return next();
  };
}

module.exports = { createAuthenticate };
