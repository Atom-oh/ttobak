import { Unzip, UnzipInflate, type UnzipFile } from 'fflate';
import { Parser } from 'saxen';

// Inspect the ZIP incrementally before the spreadsheet parser expands sparse
// coordinates into arrays. Do not trust advertised ZIP sizes or dimensions.
// Image and embedded binary parts that never hold worksheet XML, unless the
// workbook relationships name them as a worksheet.
const binaryPart = /(^|\/)media\/|\.(png|jpe?g|gif|bmp|tiff?|emf|wmf|svg|bin|vml|ico|webp)$/i;
const workbookRels = 'xl/_rels/workbook.xml.rels';

function eachEntry(input: Uint8Array, onEntry: (entry: UnzipFile) => void) {
  const zip = new Unzip(onEntry);
  zip.register(UnzipInflate);
  // Small chunks bound transient inflater allocations even for extreme ratios.
  for (let offset = 0; offset < input.length; offset += 4096) {
    const end = Math.min(offset + 4096, input.length);
    zip.push(input.subarray(offset, end), end === input.length);
  }
}

// Resolves a workbook relationship target to its ZIP entry name.
function resolveTarget(target: string): string {
  let path = target;
  try { path = decodeURIComponent(target); } catch { /* keep the raw target */ }
  const parts = (path.startsWith('/') ? path.slice(1) : `xl/${path}`).split('/');
  const out: string[] = [];
  for (const part of parts) {
    if (part === '..') out.pop();
    else if (part && part !== '.') out.push(part);
  }
  return out.join('/').toLowerCase();
}

// Worksheet part names declared by xl/_rels/workbook.xml.rels (lowercased).
function worksheetTargets(input: Uint8Array, reject: () => never): Set<string> {
  const targets = new Set<string>();
  let size = 0;
  eachEntry(input, (entry) => {
    if (entry.name !== workbookRels) return;
    const decoder = new TextDecoder('utf-8', { fatal: true });
    const parser = new Parser();
    parser.on('error', reject);
    parser.on('openTag', (qualifiedName, getAttributes, decode) => {
      if (qualifiedName.split(':').pop() !== 'Relationship') return;
      const attrs = getAttributes();
      const type = attrs.Type === undefined ? '' : decode(attrs.Type);
      if (/\/worksheet$/i.test(type) && attrs.Target !== undefined) targets.add(resolveTarget(decode(attrs.Target)));
    });
    entry.ondata = (err, chunk, final) => {
      if (err) throw err;
      size += chunk.length;
      if (size > 1024 * 1024) reject();
      parser.write(decoder.decode(chunk, { stream: !final }));
      if (final) parser.end();
    };
    entry.start();
  });
  return targets;
}

export function validateContactWorkbook(input: Uint8Array): void {
  const maxExpanded = 16 * 1024 * 1024;
  const maxRows = 2001; // header plus 2,000 contacts
  const maxColumns = 100;
  let total = 0;
  let entries = 0;
  const reject = (): never => { throw new Error('Excel 파일은 최대 2,000행, 100열 및 압축 해제 후 16MB까지 지원합니다.'); };
  const sheets = worksheetTargets(input, reject);
  eachEntry(input, (entry) => {
    if (++entries > 256 || (entry.originalSize !== undefined && entry.originalSize > maxExpanded)) reject();
    // A declared worksheet is parsed wherever it lives; every other part except
    // known binary media is parsed too. Undecodable parsed parts fail closed.
    const xml = sheets.has(entry.name.toLowerCase()) || !binaryPart.test(entry.name);
    const decoder = xml ? new TextDecoder('utf-8', { fatal: true }) : null;
    const parser = xml ? new Parser() : null;
    let rowCount = 0;
    // Only a sheetData element directly under the document root bounds the
    // sheet; a same-named element nested elsewhere (e.g. in an extension list)
    // can neither open nor close that scope.
    let depth = 0;
    let sheetDataDepth = -1;
    if (parser) {
      parser.on('error', reject);
      parser.on('openTag', (qualifiedName, getAttributes, decode) => {
        const name = qualifiedName.split(':').pop();
        depth++;
        if (name === 'sheetData' && depth === 2 && sheetDataDepth < 0) { sheetDataDepth = depth; return; }
        if (sheetDataDepth < 0) return;
        if (name !== 'row' && name !== 'c') return;
        const raw = getAttributes().r;
        const ref = raw === undefined ? '' : decode(raw);
        if (name === 'row') {
          if (++rowCount > maxRows || (ref && (!/^[1-9][0-9]*$/.test(ref) || Number(ref) > maxRows))) reject();
        } else {
          const parts = /^([A-Za-z]+)([1-9][0-9]*)$/.exec(ref);
          if (!parts) reject();
          const column = parts![1].toUpperCase().split('').reduce((n, c) => n * 26 + c.charCodeAt(0) - 64, 0);
          if (column > maxColumns || Number(parts![2]) > maxRows) reject();
        }
      });
      parser.on('closeTag', () => {
        if (depth === sheetDataDepth) sheetDataDepth = -1;
        depth--;
      });
    }
    entry.ondata = (err, chunk, final) => {
      if (err) throw err;
      total += chunk.length;
      if (total > maxExpanded) reject();
      if (parser && decoder) {
        parser.write(decoder.decode(chunk, { stream: !final }));
        if (final) parser.end();
      }
    };
    entry.start();
  });
  if (!entries) throw new Error('Excel 파일을 읽지 못했습니다.');
}
