// Bundles the HTTP MCP Lambda into one ESM file for CDK's Code.fromAsset.
// Unlike the downloadable stdio adapter, this does NOT define
// TTOBAK_STANDALONE_STDIO, because the Lambda is the hosted HTTP transport.
import { build } from 'esbuild';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
await build({
  absWorkingDir: root,
  entryPoints: ['src/lambda.ts'],
  outfile: 'dist/lambda-bundle/index.mjs',
  bundle: true,
  platform: 'node',
  target: 'node22',
  format: 'esm',
  // CommonJS dependencies in the bundle call require() on Node built-ins.
  banner: { js: "import { createRequire as __ttobakCreateRequire } from 'node:module'; const require = __ttobakCreateRequire(import.meta.url);" },
  legalComments: 'none',
  logLevel: 'info',
});
