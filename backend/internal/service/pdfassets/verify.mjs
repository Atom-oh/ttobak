import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const fontPath = new URL('./node_modules/pretendard/dist/public/static/alternative/Pretendard-Regular.ttf', import.meta.url);
export const licensePath = new URL('./node_modules/pretendard/dist/LICENSE.txt', import.meta.url);
export const fontSHA256 = '6d0af5258997aec7354a6e340fc2325ba321c410ca48b3af858c8c3d6e92a324';
export const licenseSHA256 = 'b04538c9abec39a3db75108cf0af0fd9c77032fe8aa2cf38345b4d250e98e38e';

export function verifyPDFAssets(font = fontPath, license = licensePath) {
  let fontBytes, licenseBytes;
  try {
    fontBytes = readFileSync(font);
    licenseBytes = readFileSync(license);
  } catch {
    throw new Error('PDF font dependency is missing; run npm ci --ignore-scripts in backend/internal/service/pdfassets.');
  }
  if (createHash('sha256').update(fontBytes).digest('hex') !== fontSHA256 ||
      createHash('sha256').update(licenseBytes).digest('hex') !== licenseSHA256) {
    throw new Error('PDF font dependency failed integrity verification.');
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    verifyPDFAssets();
    console.log('Pretendard 1.3.9 font and OFL license integrity verified.');
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
