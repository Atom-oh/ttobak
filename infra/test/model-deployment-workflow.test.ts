import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const workflow = readFileSync(join(__dirname, '../../.github/workflows/deploy-infra.yml'), 'utf8');
const code = workflow.split('\n').filter(line => !line.trim().startsWith('#')).join('\n');

test('both synthesis gate and deploy inherit the same validated model selections', () => {
  const sharedEnvironment = code.match(/\nenv:\n([\s\S]*?)(?=\n\w)/)?.[1];
  expect(sharedEnvironment).toBeDefined();
  for (const [environment, input] of [
    ['MODEL_DEPLOYMENT_MODE', 'model_deployment_mode'],
    ['MODEL_MEETING_SUMMARY', 'model_meeting_summary'],
    ['MODEL_TRANSLATION', 'model_translation'],
  ]) {
    // Blank manual inputs inherit persistent repository variables; the same
    // workflow-level environment reaches tests and the release coordinator.
    expect(sharedEnvironment).toContain(`${environment}: \${{ inputs.${input} || vars.${environment} }}`);
    const inputBlock = code.match(new RegExp(`      ${input}:\\n([\\s\\S]*?)(?=\\n      \\w+:|\\n\\w+:)`))?.[1];
    expect(inputBlock).toContain('required: false');
    expect(inputBlock).toContain('type: string');
    expect(inputBlock).toContain("default: ''");
  }
  // No job may shadow the verified model choice before deployment.
  const jobs = code.slice(code.indexOf('\njobs:'));
  expect(jobs).not.toMatch(/\bMODEL_(?:DEPLOYMENT_MODE|MEETING_SUMMARY|TRANSLATION):/);
  // Bind user input through environment variables, never shell interpolation.
  expect(jobs).not.toContain('inputs.model_');
});

test('model controls preserve the prerequisite job and ordered release coordinator', () => {
  const gate = code.match(/\n  infra-tests:\n([\s\S]*?)(?=\n  deploy:)/)?.[1] ?? '';
  const deploy = code.match(/\n  deploy:\n([\s\S]*)/)?.[1] ?? '';
  expect(gate).toContain('npx cdk synth');
  expect(gate).toContain('npm test');
  expect(deploy).toContain('needs: infra-tests');
  expect(deploy).toContain('http_origin_release.py release');
  expect(deploy).toContain('http_origin_release.py retighten');
  expect(deploy).toContain('if: always()');
  expect(deploy).not.toMatch(/cdk deploy.*--all/);
  expect(code).toContain('cancel-in-progress: false');
});
