import { readFileSync } from 'node:fs';
import { join } from 'node:path';

test('origin proof is not stored in CDK context or passed as application props', () => {
  const context = JSON.parse(readFileSync(join(__dirname, '../cdk.json'), 'utf8')).context;
  // Assert only presence; never print a secret-bearing context object on failure.
  expect(Object.hasOwn(context, 'ttobak:originVerifySecret')).toBe(false);
  const app = readFileSync(join(__dirname, '../bin/infra.ts'), 'utf8');
  expect(app.includes('originVerifySecret')).toBe(false);
  expect(app).toContain('httpOriginSecret: gatewayStack.httpOrigin.secret');
});
