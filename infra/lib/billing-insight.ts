import { Construct } from 'constructs';

export const BILLING_INSIGHT_URL_CONTEXT = 'ttobak:billingInsightUrl';

/**
 * Reads the optional AWS Billing Insight companion origin from CDK context.
 * Absent or empty disables the hand-off (no API env var, no config flag).
 * A set value must be an https origin with nothing beyond an optional
 * trailing "/"; anything else fails synth so the API never runs with a
 * value its own validator would reject. Returns the origin without a slash.
 */
export function billingInsightUrlContext(scope: Construct): string {
  const raw = scope.node.tryGetContext(BILLING_INSIGHT_URL_CONTEXT);
  if (raw === undefined || raw === null || raw === '') return '';
  const fail = () => new Error(
    `${BILLING_INSIGHT_URL_CONTEXT} must be an https origin without path, query or fragment, got ${JSON.stringify(raw)}`);
  if (typeof raw !== 'string' || raw !== raw.trim() || raw.includes('#') || raw.includes('?')) throw fail();
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw fail();
  }
  if (url.protocol !== 'https:' || !url.hostname || url.username || url.password
      || url.pathname !== '/' || !/^https:\/\/[^/]+\/?$/.test(raw)) {
    throw fail();
  }
  return url.origin;
}
