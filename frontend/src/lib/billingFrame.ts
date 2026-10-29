/**
 * The Billing iframe URL for a /billing location: /billing-app/upload.html when
 * ?view=upload, else /billing-app/index.html, carrying ?acct=<id> only when the id
 * matches the BFF account id pattern. Pure and dependency-free so it can be unit tested.
 */

// Same pattern as ACCT_ID in services/billing/bff/lib/store.js (a unit test keeps them equal).
export const BILLING_ACCT_ID = /^[A-Za-z0-9_-]{1,64}$/;

export function billingFrameSrc(search: string): string {
  const q = new URLSearchParams(search);
  const path = q.get('view') === 'upload' ? '/billing-app/upload.html' : '/billing-app/index.html';
  const acct = q.get('acct');
  if (!acct || !BILLING_ACCT_ID.test(acct)) return path;
  return path + '?' + new URLSearchParams({ acct }).toString();
}
