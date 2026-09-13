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
