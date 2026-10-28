import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';

export interface ContentSecurityPolicyOptions {
  // Default "'none'". The Billing pages use "'self'" so /billing can frame them.
  frameAncestors?: string;
  // When set, script-src is 'self' plus exactly these hash sources: no
  // 'unsafe-inline' and no 'wasm-unsafe-eval'.
  scriptHashes?: string[];
}

// The remote Tauri window consumes these HTTP headers too. Its local config
// CSP does not replace the CloudFront policy.
export function applicationContentSecurityPolicy(
  region: string, domain: string, assetsBucket: string, kbBucket: string, cloudfrontDomain?: string,
  options: ContentSecurityPolicyOptions = {},
): string {
  const assets = [
    `https://${assetsBucket}.s3.${region}.amazonaws.com`,
    `https://${assetsBucket}.s3.amazonaws.com`,
  ];
  const kbUploads = [
    `https://${kbBucket}.s3.${region}.amazonaws.com`,
    `https://${kbBucket}.s3.amazonaws.com`,
  ];
  const applicationHosts = [domain, ...(cloudfrontDomain ? [cloudfrontDomain] : [])];
  const applicationOrigins = applicationHosts.map(host => `https://${host}`);
  return [
    "default-src 'self'",
    "base-uri 'self'",
    "object-src 'none'",
    `frame-ancestors ${options.frameAncestors ?? "'none'"}`,
    "form-action 'self'",
    // Static Next exports include hydration/bootstrap inline scripts. Tauri
    // injects its bridge, and Shiki compiles WASM. No general JS eval is allowed.
    options.scriptHashes
      ? ['script-src', "'self'", ...options.scriptHashes].join(' ')
      : "script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'",
    "style-src 'self' 'unsafe-inline'",
    "font-src 'self' data:",
    "img-src 'self' data: blob: https:",
    `media-src 'self' blob: asset: ${[...applicationOrigins, ...assets].join(' ')}`,
    `frame-src 'self' blob: ${[...applicationOrigins, ...assets].join(' ')}`,
    "worker-src 'self' blob:",
    [
      "connect-src 'self'",
      ...applicationOrigins,
      ...applicationHosts.map(host => `wss://${host}`),
      `https://cognito-idp.${region}.amazonaws.com`,
      `https://cognito-identity.${region}.amazonaws.com`,
      `wss://transcribestreaming.${region}.amazonaws.com:8443`,
      'ipc: http://ipc.localhost',
      ...assets,
      ...kbUploads,
    ].join(' '),
  ].join('; ');
}

// CSP hash sources for every inline <script> (one without src) in the given
// HTML files, in document order. Browsers hash the element text after HTML
// input normalization, which turns CRLF and lone CR into LF.
export function inlineScriptHashes(files: string[]): string[] {
  const hashes: string[] = [];
  for (const file of files) {
    const html = readFileSync(file, 'utf8').replace(/\r\n?/g, '\n');
    for (const m of html.matchAll(/<script(\s[^>]*)?>([\s\S]*?)<\/script>/gi)) {
      if (/\ssrc\s*=/i.test(m[1] ?? '')) continue;
      hashes.push(`'sha256-${createHash('sha256').update(m[2], 'utf8').digest('base64')}'`);
    }
  }
  return hashes;
}
