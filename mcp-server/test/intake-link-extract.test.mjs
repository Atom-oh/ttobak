// Slack link extraction and the public-link filter. All data is synthetic.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { INTERNAL_HOST_SUFFIXES, extractUrls, publicUrl } from '../dist/intake/link-extract.js';

test('internal suffixes equal the shared contract', () => {
  const path = fileURLToPath(new URL('../../contracts/customer-updates/internal-hosts-v1.json', import.meta.url));
  assert.deepEqual([...INTERNAL_HOST_SUFFIXES], JSON.parse(readFileSync(path, 'utf8')).suffixes);
});

test('extractUrls reads bracket and bare forms', () => {
  assert.deepEqual(extractUrls('see <https://a.example/x|read this> and https://b.example/y.'),
    ['https://a.example/x', 'https://b.example/y']);
  assert.deepEqual(extractUrls('<https://a.example/x?p=1&amp;q=2>'), ['https://a.example/x?p=1&q=2']);
});

test('extractUrls de-duplicates across forms', () => {
  assert.deepEqual(extractUrls('<https://a.example/x|label> then https://a.example/x'), ['https://a.example/x']);
});

test('extractUrls ignores non-URL Slack tokens', () => {
  assert.deepEqual(extractUrls('<mailto:a@b.c> <#C123|name> <@U123>'), []);
});

test('extractUrls caps a message at 20 URLs', () => {
  const text = Array.from({ length: 25 }, (_, i) => `https://a.example/${i}`).join(' ');
  const urls = extractUrls(text);
  assert.equal(urls.length, 20);
  assert.equal(urls[0], 'https://a.example/0');
  assert.equal(urls[19], 'https://a.example/19');
});

test('extractUrls strips trailing punctuation but keeps a balanced paren', () => {
  assert.deepEqual(extractUrls('(see https://a.example/x), ok'), ['https://a.example/x']);
  assert.deepEqual(extractUrls('https://a.example/x!?]'), ['https://a.example/x']);
  assert.deepEqual(extractUrls('read https://en.wikipedia.org/wiki/A_(b).'), ['https://en.wikipedia.org/wiki/A_(b)']);
  assert.deepEqual(extractUrls('https://en.wikipedia.org/wiki/A_(b)'), ['https://en.wikipedia.org/wiki/A_(b)']);
});

test('publicUrl accepts a public https link unchanged', () => {
  const url = 'https://aws.amazon.com/about-aws/whats-new/2026/11/x/';
  assert.equal(publicUrl(url), url);
  assert.equal(publicUrl(`  ${url}  `), url);
});

test('publicUrl rejects non-https and user info', () => {
  assert.equal(publicUrl('http://a.example/x'), null);
  assert.equal(publicUrl('ftp://a.example/x'), null);
  assert.equal(publicUrl('https://u:p@a.example/'), null);
  assert.equal(publicUrl('https://u@a.example/'), null);
});

test('publicUrl rejects internal hosts by suffix', () => {
  assert.equal(publicUrl('https://team.slack.com/archives/C1/p1'), null);
  assert.equal(publicUrl('https://w.amazon.com/bin/view/X'), null);
  assert.equal(publicUrl('https://SLACK.com/x'), null);
  assert.equal(publicUrl('https://a.b.sharepoint.com/x'), null);
  assert.equal(publicUrl('https://notslack.com/x'), 'https://notslack.com/x');
  assert.equal(publicUrl('https://amazon.com/x'), 'https://amazon.com/x');
});

test('publicUrl rejects IPs, numeric tails, bracketed IPv6 and dotless hosts', () => {
  assert.equal(publicUrl('https://10.0.0.1/x'), null);
  assert.equal(publicUrl('https://127.1/x'), null);
  assert.equal(publicUrl('https://1.2.3/x'), null);
  assert.equal(publicUrl('https://0x7f.0x1/x'), null);
  assert.equal(publicUrl('https://[::1]/x'), null);
  assert.equal(publicUrl('https://[2001:db8::1]:8443/x'), null);
  assert.equal(publicUrl('https://localhost/x'), null);
});

test('publicUrl rejects whitespace, control characters and long URLs', () => {
  assert.equal(publicUrl('https://a.example/x y'), null);
  assert.equal(publicUrl('https://a.example/x\u0000'), null);
  assert.equal(publicUrl('https://a.example/x\u0085'), null);
  assert.equal(publicUrl(''), null);
  const prefix = 'https://a.example/';
  assert.equal(publicUrl(prefix + 'a'.repeat(2048 - prefix.length)).length, 2048);
  assert.equal(publicUrl(prefix + 'a'.repeat(2049 - prefix.length)), null);
});

test('publicUrl counts its 2,048 limit in UTF-8 bytes', () => {
  const prefix = 'https://a.example/';
  // '가' is three bytes: 676 of them, the prefix and "aa" are 2,048 bytes; 677 and the prefix are 2,049.
  const fits = prefix + '가'.repeat(676) + 'aa';
  assert.equal(Buffer.byteLength(fits, 'utf8'), 2048);
  assert.equal(publicUrl(fits), fits);
  assert.equal(publicUrl(prefix + '가'.repeat(677)), null);
});

test('extractUrls does not take a bracket span with whitespace as a Slack token', () => {
  assert.deepEqual(extractUrls('if a < b see https://a.example/x > c'), ['https://a.example/x']);
  assert.deepEqual(extractUrls('<https://a.example/x y|label>'), ['https://a.example/x']);
  assert.deepEqual(extractUrls('<https://a.example/z|read this label>'), ['https://a.example/z']);
});

test('extractUrls drops an escaped ">" after a bare URL', () => {
  assert.deepEqual(extractUrls('-&gt; https://a.example/x&gt;'), ['https://a.example/x']);
  assert.deepEqual(extractUrls('https://a.example/y.&gt;&gt;'), ['https://a.example/y']);
});

test('a bare URL containing "|" is kept whole (pinned)', () => {
  assert.deepEqual(extractUrls('see https://a.example/a|b now'), ['https://a.example/a|b']);
});
