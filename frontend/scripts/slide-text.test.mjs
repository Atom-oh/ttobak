import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import * as fflate from 'fflate';

const source = readFileSync(new URL('../src/lib/slideText.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
});
const slideText = {};
vm.runInNewContext(outputText, {
  exports: slideText,
  require: (name) => {
    assert.equal(name, 'fflate');
    return fflate;
  },
  TextDecoder, Uint8Array,
});

const slide = (...paragraphs) => `<p:sld><p:txBody>${paragraphs.map((runs) =>
  `<a:p>${runs.map((run) => `<a:r><a:rPr lang="ko-KR"/><a:t>${run}</a:t></a:r>`).join('')}</a:p>`).join('')}</p:txBody></p:sld>`;

function pptx(slides, extra = {}) {
  const files = { '[Content_Types].xml': fflate.strToU8('<Types/>'), ...extra };
  slides.forEach((xml, at) => { files[`ppt/slides/slide${at + 1}.xml`] = fflate.strToU8(xml); });
  return fflate.zipSync(files);
}

const asFile = (name, bytes) => ({
  name, size: bytes.length,
  arrayBuffer: async () => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength),
});

test('slide XML: runs join into paragraphs and entities decode', () => {
  const xml = slide(['Java ', '런타임'], ['R&amp;D &lt;Nginx&gt; &#44032;&#x41;'], ['   ']);
  assert.equal(slideText.slideXmlText(xml), 'Java 런타임\nR&D <Nginx> 가A');
});

test('pptx text comes out in slide order, from slides only', () => {
  const slides = Array.from({ length: 11 }, (_, at) => slide([`슬라이드 ${at + 1}`]));
  const bytes = pptx(slides, { 'ppt/notesSlides/notesSlide1.xml': fflate.strToU8(slide(['발표자 노트'])) });
  const text = slideText.pptxText(bytes);
  assert.deepEqual(text.split('\n').slice(0, 3), ['슬라이드 1', '슬라이드 2', '슬라이드 3']);
  assert.equal(text.split('\n').at(-1), '슬라이드 11');
  assert.equal(text.includes('발표자 노트'), false);
});

test('an archive without slides and a non-archive are rejected', async () => {
  assert.throws(() => slideText.pptxText(fflate.zipSync({ 'a.txt': fflate.strToU8('x') })));
  await assert.rejects(slideText.slideFileText(asFile('deck.pptx', fflate.strToU8('not a zip'))), { code: 'unreadable' });
  await assert.rejects(slideText.slideFileText(asFile('deck.pdf', fflate.strToU8('x'))), { code: 'unsupported' });
  await assert.rejects(slideText.slideFileText(asFile('notes.txt', fflate.strToU8('   '))), { code: 'empty' });
  await assert.rejects(
    slideText.slideFileText({ name: 'big.pptx', size: slideText.MAX_SLIDE_FILE_BYTES + 1, arrayBuffer: async () => new ArrayBuffer(0) }),
    { code: 'too-large' },
  );
});

test('text files are read as they are and long text is cut on a character', async () => {
  const short = await slideText.slideFileText(asFile('terms.MD', fflate.strToU8('Nginx, PHP 런타임')));
  assert.deepEqual({ ...short }, { text: 'Nginx, PHP 런타임', truncated: false });
  const long = await slideText.slideFileText(asFile('terms.txt', fflate.strToU8('가'.repeat(30_000))));
  assert.equal(long.truncated, true);
  assert.equal(long.text, '가'.repeat(20_000));
  assert.deepEqual({ ...slideText.utf8Head('a😀b', 4) }, { text: 'a', truncated: true });
});

test('every error code has a Korean message', () => {
  for (const code of ['unsupported', 'too-large', 'unreadable', 'empty']) {
    assert.ok(slideText.slideTextErrorMessage(code).length > 0, code);
  }
  assert.equal(slideText.slideTextErrorMessage('timeout'), '');
});
