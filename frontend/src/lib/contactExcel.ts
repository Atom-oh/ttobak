import { Unzip, UnzipInflate } from 'fflate';
import { Parser } from 'saxen';

// Inspect the ZIP incrementally before the spreadsheet parser expands sparse
// coordinates into arrays. Do not trust advertised ZIP sizes or dimensions.
export function validateContactWorkbook(input: Uint8Array): void {
  const maxExpanded = 16 * 1024 * 1024;
  const maxRows = 2001; // header plus 2,000 contacts
  const maxColumns = 100;
  let total = 0;
  let entries = 0;
  const reject = () => { throw new Error('Excel 파일은 최대 2,000행, 100열 및 압축 해제 후 16MB까지 지원합니다.'); };
  const zip = new Unzip((entry) => {
    if (++entries > 256 || (entry.originalSize !== undefined && entry.originalSize > maxExpanded)) reject();
    // Relationships can point a sheet at any XML path within the workbook.
    const xml = /\.xml$/i.test(entry.name);
    const decoder = xml ? new TextDecoder('utf-8', { fatal: true }) : null;
    const parser = xml ? new Parser() : null;
    let rowCount = 0;
    let inSheetData = false;
    if (parser) {
      parser.on('error', reject);
      parser.on('openTag', (qualifiedName, getAttributes, decode) => {
        const name = qualifiedName.split(':').pop();
        if (name === 'sheetData') { inSheetData = true; return; }
        if (!inSheetData) return;
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
      parser.on('closeTag', (name) => { if (name.split(':').pop() === 'sheetData') inSheetData = false; });
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
  zip.register(UnzipInflate);
  // Small chunks bound transient inflater allocations even for extreme ratios.
  for (let offset = 0; offset < input.length; offset += 4096) {
    const end = Math.min(offset + 4096, input.length);
    zip.push(input.subarray(offset, end), end === input.length);
  }
  if (!entries) throw new Error('Excel 파일을 읽지 못했습니다.');
}
