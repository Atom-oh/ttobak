import { readFileSync } from 'node:fs';
import { join } from 'node:path';

// bin/infra.ts is not executed by Jest; pin that the full app refuses to
// synthesize without the CloudFront origin proof.
test('the app requires ttobak:originVerifySecret instead of defaulting to empty', () => {
  const code = readFileSync(join(__dirname, '../bin/infra.ts'), 'utf8');
  expect(code).not.toMatch(/tryGetContext\('ttobak:originVerifySecret'\)\s*\|\|\s*''/);
  expect(code).toMatch(/if \(typeof originVerifySecret !== 'string' \|\| originVerifySecret\.length < 16\) \{\s*throw new Error/);
});
