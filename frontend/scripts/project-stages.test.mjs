import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const stages = {};
vm.runInNewContext(ts.transpileModule(readFileSync(new URL('../src/lib/projectStages.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText, { exports: stages });

test('terminal_stages', () => {
  assert.equal(stages.isTerminalStage('Closed Lost'), true);
  assert.equal(stages.isTerminalStage('Launched'), true);
  assert.equal(stages.isTerminalStage('Committed'), false);
  assert.equal(stages.isTerminalStage(undefined), false);
});

test('unmapped_stage_shows_unset_label', () => {
  assert.equal(stages.stageLabel('POC', false), stages.UNSET_STAGE_LABEL);
  assert.equal(stages.stageLabel('Qualified', true), 'Qualified');
  assert.equal(stages.stageLabel('', true), '');
});

test('stage_vocabulary_is_fixed', () => {
  assert.equal(stages.PROJECT_STAGES.length, 7);
  assert.equal(stages.PROJECT_STAGES[0], 'Prospect');
  assert.equal(stages.PROJECT_STAGES[6], 'Closed Lost');
});
