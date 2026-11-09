import { parseContactCsv } from './contactImport';

export const CONTACT_FILE_MAX_BYTES = 5 * 1024 * 1024;
export const CONTACT_FILE_MAX_ROWS = 2000;

/** Reads a Remember CSV (UTF-8 or EUC-KR) or .xlsx export into rows; the first row is the header. */
export async function readContactFile(file: File): Promise<string[][]> {
  if (file.size > CONTACT_FILE_MAX_BYTES) throw new Error('파일은 5MB 이하여야 합니다.');
  let rows: string[][];
  if (/\.xlsx$/i.test(file.name)) {
    const [{ readSheet }, { validateContactWorkbook }] = await Promise.all([import('read-excel-file/browser'), import('./contactExcel')]);
    const bytes = await file.arrayBuffer();
    validateContactWorkbook(new Uint8Array(bytes));
    const sheet = await readSheet(bytes);
    rows = sheet.map((row) => row.map((cell) => cell instanceof Date ? cell.toISOString().slice(0, 10) : cell == null ? '' : String(cell)));
  } else if (/\.csv$/i.test(file.name)) {
    const bytes = await file.arrayBuffer();
    let text: string;
    try { text = new TextDecoder('utf-8', { fatal: true }).decode(bytes); }
    catch { text = new TextDecoder('euc-kr').decode(bytes); }
    rows = parseContactCsv(text);
  } else throw new Error('CSV 또는 .xlsx 파일을 선택해 주세요.');
  if (rows.length < 2) throw new Error('제목 행과 연락처 행이 필요합니다.');
  if (rows.length - 1 > CONTACT_FILE_MAX_ROWS || rows.some((row) => row.length > 100)) throw new Error('한 번에 최대 2,000행, 100열까지 가져올 수 있습니다.');
  return [rows[0].map((header, i) => header.trim() || `열 ${i + 1}`), ...rows.slice(1)];
}
