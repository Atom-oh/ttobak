import { execFileSync } from 'node:child_process';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const policyUrl = pathToFileURL(resolve(__dirname, '../lambda/pre-signup/policy.mjs')).href;
function approved(triggerSource: string, email: unknown): boolean {
  // Execute the same native ESM module packaged for Lambda, without AWS access.
  const script = `
    import { isApprovedAdminInvite } from ${JSON.stringify(policyUrl)};
    process.stdout.write(JSON.stringify(isApprovedAdminInvite(JSON.parse(process.argv[1]))));
  `;
  const event = { triggerSource, request: { userAttributes: { email } } };
  return JSON.parse(execFileSync(process.execPath, ['--input-type=module', '-e', script, JSON.stringify(event)],
    { encoding: 'utf8' }));
}

describe('approved demo invite', () => {
  test.each(['demo@atomai.click', 'Demo@Atomai.Click'])('accepts the exact administrator-created address: %s', (email) => {
    expect(approved('PreSignUp_AdminCreateUser', email)).toBe(true);
  });
  test.each(['PreSignUp_SignUp', 'PreSignUp_ExternalProvider', '', 'AdminCreateUser'])(
    'does not bypass the domain policy for trigger %s', (trigger) => {
      expect(approved(trigger, 'demo@atomai.click')).toBe(false);
    });
  test.each(['other@atomai.click', 'demo+test@atomai.click', 'demo@sub.atomai.click',
    'demo@atomai.click.evil.example', ' demo@atomai.click', '', null])(
    'does not grant another address an exception: %s', (email) => {
      expect(approved('PreSignUp_AdminCreateUser', email)).toBe(false);
    });
});
