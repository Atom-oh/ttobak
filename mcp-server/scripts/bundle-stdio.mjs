// Bundles the downloadable stdio adapter into one ESM file
// (dist/ttobak-mcp.mjs, published as frontend/public/mcp/ttobak-mcp.mjs).
// TTOBAK_STANDALONE_STDIO drops the HTTP transport from this artifact.
import { build } from 'esbuild';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
await build({
  absWorkingDir: root,
  entryPoints: ['src/index.ts'],
  outfile: 'dist/ttobak-mcp.mjs',
  bundle: true,
  platform: 'node',
  target: 'node18',
  format: 'esm',
  // CommonJS dependencies (e.g. cross-spawn under the SDK's stdio client, used
  // by `intake`) call require() on Node built-ins, which ESM lacks.
  banner: {
    js: "#!/usr/bin/env node\nimport { createRequire as __ttobakCreateRequire } from 'node:module'; const require = __ttobakCreateRequire(import.meta.url);",
  },
  define: { TTOBAK_STANDALONE_STDIO: 'true' },
  logLevel: 'info',
});
