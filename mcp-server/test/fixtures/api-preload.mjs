// Loaded only into the regression-test child via node --import. Exercise the
// real stdio server without reading credentials, sending network requests, or
// persisting any customer data.
import { TtobakApi } from '../../dist/api.js';
import { CognitoAuth } from '../../dist/auth.js';

CognitoAuth.prototype.loadTokens = function () {};
const saved = new Map();
TtobakApi.prototype.request = async function (method, path, body) {
  if (path.endsWith('/shared-doc') && method === 'PUT') throw new Error('HTTP 404: NOT_FOUND');
  const request = { method, path, body };
  if (path === '/api/documents' && method === 'POST') {
    const doc = { docId: 'doc-1', title: body.title, content: body.markdown };
    saved.set(doc.docId, doc);
    return { ...doc, request };
  }
  if (path === '/api/documents' && method === 'GET') {
    return { documents: [...saved.values()], request };
  }
  if (path === '/api/documents/doc-1') {
    const doc = saved.get('doc-1');
    if (method === 'PUT') {
      Object.assign(doc, { title: body.title }, body.markdown !== undefined ? { content: body.markdown } : {});
    }
    return { ...doc, request };
  }
  return { request };
};
