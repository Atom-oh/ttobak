'use strict';
// services/billing/page/body.html is imported across stacked PRs because it exceeds
// the review window. A staged fragment is allowed only while nothing consumes the
// file; once the marker is gone, every inline script must compile.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const PAGE = path.join(__dirname, '..', '..', 'page');
const MARKER = '// BILLING-STAGED-FRAGMENT';
const CLOSED = /<script>([\s\S]*?)<\/script\s*>/gi;

test('staged body.html has no consumer; complete body.html compiles', () => {
  const body = fs.readFileSync(path.join(PAGE, 'body.html'), 'utf8');
  if (body.includes(MARKER)) {
    assert.equal(fs.existsSync(path.join(PAGE, 'build_page.py')), false, 'a staged fragment must not be consumed');
    return;
  }
  const scripts = [...body.matchAll(CLOSED)].map((m) => m[1]);
  assert.ok(scripts.length > 0, 'no inline scripts found');
  for (const src of scripts) assert.doesNotThrow(() => new vm.Script(src), 'inline script does not compile');
  assert.equal(/<script(?![^>]*\ssrc=)[^>]*>/i.test(body.replace(CLOSED, '')), false, 'unclosed inline <script>');
});
