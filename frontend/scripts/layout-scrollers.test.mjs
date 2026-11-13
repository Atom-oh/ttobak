import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

// An absolutely positioned descendant (`sr-only`) of a scroller that is not a
// containing block is laid out against the body, escapes the layout's clip and
// lengthens the document, so the window scrolls behind the app shell. Both
// shell scrollers must therefore be positioned.
const read = path => readFileSync(new URL(`../src/components/layout/${path}`, import.meta.url), 'utf8');

for (const [file, marker] of [['AppLayout.tsx', 'overflow-y-auto animate-in'], ['Sidebar.tsx', 'min-h-0 overflow-y-auto']]) {
  test(`${file} scroller is a containing block`, () => {
    const classes = [...read(file).matchAll(/className="([^"]*)"/g)].map(match => match[1])
      .filter(value => value.includes(marker));
    assert.equal(classes.length, 1);
    assert.ok(classes[0].split(' ').includes('relative'), classes[0]);
  });
}
