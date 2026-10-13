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

test.each([
  ['.github/workflows/test-backend.yml', 'GOOS=linux GOARCH=arm64 go build'],
  ['.github/workflows/deploy-infra.yml', 'GOOS=linux GOARCH=arm64 go build'],
  ['.github/workflows/evaluate-notes.yml', 'go -C backend test'],
  ['scripts/build.sh', 'GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build'],
  ['scripts/setup.sh', 'GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build'],
  ['scripts/eval-note-quality.sh', 'exec "$go_binary" -C'],
])('%s restores and verifies the pinned font before compiling service', (file, compile) => {
  const code = readFileSync(join(__dirname, '../..', file), 'utf8')
    .split('\n').filter(line => !line.trim().startsWith('#')).join('\n');
  const restore = code.search(/npm --prefix [^\n]*backend\/internal\/service\/pdfassets"? ci --ignore-scripts --no-audit --no-fund/);
  const verify = code.search(/npm --prefix [^\n]*backend\/internal\/service\/pdfassets"? run verify/);
  expect(restore).toBeGreaterThan(-1);
  expect(verify).toBeGreaterThan(restore);
  expect(code.indexOf(compile)).toBeGreaterThan(verify);
});

test.each([
  '.github/workflows/test-backend.yml',
  '.github/workflows/deploy-infra.yml',
  'scripts/build.sh',
  'scripts/setup.sh',
])('%s includes the readable upstream font license in the API artifact', (file) => {
  const code = readFileSync(join(__dirname, '../..', file), 'utf8');
  expect(code).toContain('cp internal/service/pdfassets/node_modules/pretendard/dist/LICENSE.txt cmd/api/Pretendard-OFL.txt');
});

test('convert-doc restores the pinned dependency inside its build before Go embedding', () => {
  const code = readFileSync(join(__dirname, '../../backend/cmd/convert-doc/Dockerfile'), 'utf8');
  const restore = code.indexOf('RUN npm ci --ignore-scripts --no-audit --no-fund && npm run verify');
  const dependency = code.indexOf('COPY --from=pdf-fonts /pdfassets/node_modules ./internal/service/pdfassets/node_modules');
  const compile = code.indexOf('RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build');
  expect(restore).toBeGreaterThan(-1);
  expect(dependency).toBeGreaterThan(restore);
  expect(compile).toBeGreaterThan(dependency);
});
