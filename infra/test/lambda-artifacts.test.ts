import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const zipTargets = [
  'cmd/api', 'cmd/transcribe', 'cmd/summarize', 'cmd/process-image',
  'cmd/kb', 'cmd/research-worker', 'cmd/websocket', 'cmd/ws-authorizer',
].sort();

test.each([
  '.github/workflows/deploy-infra.yml',
  '.github/workflows/test-backend.yml',
  'scripts/build.sh',
])('%s packages all declared zip handlers, including WS authorization', (file) => {
  const code = readFileSync(join(__dirname, '../..', file), 'utf8')
    .split('\n').filter(line => !line.trim().startsWith('#')).join('\n');
  const loop = code.match(/for dir in ([^\n;]+); do/);
  expect(loop).not.toBeNull();
  expect(loop![1].trim().split(/\s+/).sort()).toEqual(zipTargets);
  expect(code).toMatch(/GOOS=linux GOARCH=arm64 (?:\/usr\/local\/go\/bin\/)?go build -tags lambda\.norpc -o "\$dir\/bootstrap" "\.\/\$dir"/);
  expect(zipTargets).not.toContain('cmd/convert-doc');
});

// GatewayStack's ttobak-mcp function reads Code.fromAsset('../mcp-server/dist/lambda-bundle'),
// a gitignored build output. Every path that synthesizes or deploys must build it first.
test.each([
  ['.github/workflows/deploy-infra.yml', 2],
  ['.github/workflows/test-infra.yml', 1],
])('%s builds the HTTP MCP Lambda bundle before synth', (file, jobs) => {
  const code = readFileSync(join(__dirname, '../..', file), 'utf8')
    .split('\n').filter(line => !line.trim().startsWith('#')).join('\n');
  expect(code.split('npm run bundle:lambda').length - 1).toBe(jobs);
  for (const job of code.split(/\n  [a-z-]+:\n    runs-on:/).slice(1)) {
    if (!job.includes('cdk synth') && !job.includes('cdk deploy')) continue;
    const bundle = job.indexOf('npm run bundle:lambda');
    expect(bundle).toBeGreaterThan(-1);
    expect(bundle).toBeLessThan(Math.min(...['cdk synth', 'cdk deploy'].map(cmd => job.indexOf(cmd)).filter(i => i >= 0)));
  }
  expect(code).toMatch(/paths:[\s\S]*'mcp-server\/\*\*'/);
});

test('scripts/build.sh builds the HTTP MCP Lambda bundle in the all target', () => {
  const code = readFileSync(join(__dirname, '../..', 'scripts/build.sh'), 'utf8');
  expect(code).toMatch(/build_mcp\(\) \{[\s\S]*npm run bundle:lambda/);
  expect(code).toMatch(/all\)\s+build_backend; build_mcp; build_frontend/);
});
