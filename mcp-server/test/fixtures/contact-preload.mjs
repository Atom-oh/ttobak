import { TtobakApi } from '../../dist/api.js';
import { CognitoAuth } from '../../dist/auth.js';

CognitoAuth.prototype.loadTokens = function () {};
TtobakApi.prototype.request = async function (method, path, body, maxBytes) {
  if (maxBytes !== 32000) throw new Error('missing byte limit');
  if (method === 'GET' && path === '/api/accounts/acct/contacts?limit=25') {
    return { contacts: [{ contactId: 'existing', name: 'Existing', phone: '02-1234' }], nextCursor: 'page2' };
  }
  if (method === 'GET' && path === '/api/accounts/acct/contacts?limit=25&cursor=page2') {
    return { contacts: [{ contactId: 'existing2', name: 'Existing 2', email: 'known@example.com' }] };
  }
  if (method === 'POST' && path === '/api/accounts/acct/contacts' && body.name === 'New') return { contactId: 'created', ...body };
  throw new Error('HTTP 403 FORBIDDEN: denied');
};
