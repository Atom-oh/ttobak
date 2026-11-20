import { unzipSync, strFromU8 } from 'fflate';

// Text of a presentation, read in the browser, for caption term hints. The
// file never leaves the page; only the extracted text is handed on.

/** Longest slide text handed on, in UTF-8 bytes; matches `MAX_SLIDES_BYTES` in the Mac app. */
export const MAX_SLIDES_BYTES = 60_000;
/** Largest presentation file read. */
export const MAX_SLIDE_FILE_BYTES = 200 * 1024 * 1024;
/** Largest single slide XML inflated, so a crafted archive cannot exhaust memory. */
const MAX_SLIDE_XML_BYTES = 8 * 1024 * 1024;
const MAX_SLIDES = 500;

const SLIDE_PATH = /^ppt\/slides\/slide(\d+)\.xml$/;

function decodeXml(text: string): string {
  return text.replace(/&(amp|lt|gt|quot|apos|#\d{1,7}|#x[0-9a-fA-F]{1,6});/g, (whole, name: string) => {
    switch (name) {
      case 'amp': return '&';
      case 'lt': return '<';
      case 'gt': return '>';
      case 'quot': return '"';
      case 'apos': return "'";
    }
    const code = name[1] === 'x' ? parseInt(name.slice(2), 16) : parseInt(name.slice(1), 10);
    return code > 0 && code <= 0x10ffff && !(code >= 0xd800 && code <= 0xdfff) ? String.fromCodePoint(code) : whole;
  });
}

/** Text runs of one slide's XML, one paragraph per line. */
export function slideXmlText(xml: string): string {
  const lines: string[] = [];
  for (const paragraph of xml.split(/<\/a:p>/)) {
    const runs = Array.from(paragraph.matchAll(/<a:t(?:\s[^>]*)?>([^<]*)<\/a:t>/g), (match) => decodeXml(match[1]));
    const line = runs.join('').replace(/\s+/g, ' ').trim();
    if (line) lines.push(line);
  }
  return lines.join('\n');
}

/** Slide text of a .pptx archive, in slide order. Throws when it is not one. */
export function pptxText(bytes: Uint8Array): string {
  let count = 0;
  const files = unzipSync(bytes, {
    filter: (file) => SLIDE_PATH.test(file.name) && file.originalSize <= MAX_SLIDE_XML_BYTES && ++count <= MAX_SLIDES,
  });
  const slides = Object.entries(files)
    .map(([name, data]) => ({ index: Number(SLIDE_PATH.exec(name)?.[1] ?? 0), data }))
    .sort((a, b) => a.index - b.index);
  if (slides.length === 0) throw new Error('No slides found');
  return slides.map((slide) => slideXmlText(strFromU8(slide.data))).filter(Boolean).join('\n');
}

/** Longest head of `text` within `maxBytes` UTF-8 bytes, cut on a character boundary. */
export function utf8Head(text: string, maxBytes: number): { text: string; truncated: boolean } {
  let bytes = 0;
  let end = 0;
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0;
    const size = code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
    if (bytes + size > maxBytes) return { text: text.slice(0, end), truncated: true };
    bytes += size;
    end += char.length;
  }
  return { text, truncated: false };
}

export type SlideFileKind = 'pptx' | 'text';

/** Kind of a slide file by its name, or null when it is not supported. */
export function slideFileKind(name: string): SlideFileKind | null {
  const lower = name.toLowerCase();
  if (lower.endsWith('.pptx')) return 'pptx';
  if (lower.endsWith('.txt') || lower.endsWith('.md')) return 'text';
  return null;
}

export type SlideTextError = 'unsupported' | 'too-large' | 'unreadable' | 'empty';

/** Reads a presentation's text, cut to `MAX_SLIDES_BYTES`. Rejects with a `SlideTextError` code. */
export async function slideFileText(
  file: { name: string; size: number; arrayBuffer: () => Promise<ArrayBuffer> },
): Promise<{ text: string; truncated: boolean }> {
  const kind = slideFileKind(file.name);
  if (!kind) throw Object.assign(new Error('Unsupported file'), { code: 'unsupported' satisfies SlideTextError });
  if (file.size > MAX_SLIDE_FILE_BYTES) throw Object.assign(new Error('File too large'), { code: 'too-large' satisfies SlideTextError });
  let text: string;
  try {
    const bytes = new Uint8Array(await file.arrayBuffer());
    text = kind === 'pptx' ? pptxText(bytes) : new TextDecoder().decode(bytes);
  } catch {
    throw Object.assign(new Error('Unreadable file'), { code: 'unreadable' satisfies SlideTextError });
  }
  if (!text.trim()) throw Object.assign(new Error('No text'), { code: 'empty' satisfies SlideTextError });
  return utf8Head(text, MAX_SLIDES_BYTES);
}

export function slideTextErrorMessage(code: string): string {
  switch (code) {
    case 'unsupported': return 'PPTX, TXT, MD 파일만 올릴 수 있어요.';
    case 'too-large': return '파일이 너무 커요. 200MB 이하 파일을 올려 주세요.';
    case 'unreadable': return '파일을 읽지 못했어요. 다른 파일로 다시 시도해 주세요.';
    case 'empty': return '파일에서 글자를 찾지 못했어요.';
    default: return '';
  }
}
