export interface ContentSecurityPolicyOptions {
  // Default "'none'". The Billing pages use "'self'" so /billing can frame them.
  frameAncestors?: string;
  // Pages without inline scripts or WASM (the Billing pages): script-src 'self' only.
  scriptSelfOnly?: boolean;
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
    options.scriptSelfOnly ? "script-src 'self'" : "script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'",
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

