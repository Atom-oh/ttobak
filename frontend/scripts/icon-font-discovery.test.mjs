import assert from 'node:assert/strict';
import { test } from 'node:test';
import { collectIcons } from './update-icon-font.mjs';

test('ARIA roles are not icons; actual icon text and helper literals are discovered', () => {
  const catalog = 'switch e001\nassistant e002\ntask_alt e003\n';
  const icons = collectIcons([{
    path: 'panel.tsx',
    text: '<button role="switch"><span className="material-symbols-outlined">assistant</span></button>; const card = { icon: "task_alt" };',
  }], catalog);
  assert.deepEqual(icons, ['assistant', 'task_alt']);
  assert.deepEqual(collectIcons([{ path: 'icon.tsx', text: '<span>switch</span>' }], catalog), ['switch']);
});
