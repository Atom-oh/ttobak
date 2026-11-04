import { build } from 'esbuild';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
export async function buildConnector(outfile = resolve(root, 'build/connector-policy.cjs')) {
  await build({
    entryPoints: [resolve(root, '../../..', 'mcp-server/src/connector-policy-entry.ts')],
    outfile, bundle: true, platform: 'node', target: 'node24', format: 'cjs',
    external: ['@aws-sdk/*'],
  });
}
if (process.argv[1] === fileURLToPath(import.meta.url)) await buildConnector();
