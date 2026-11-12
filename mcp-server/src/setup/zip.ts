// A bounded zip reader for skill packages: stored and deflate entries only, no
// zip64, no encryption. Names and modes are checked by package.ts; read()
// never returns more than an entry's declared size.

import { inflateRawSync } from 'node:zlib';
import { SetupError } from './errors.js';

export const MAX_ZIP_BYTES = 20 * 1024 * 1024;
export const MAX_ENTRIES = 500;
export const MAX_UNCOMPRESSED = 50 * 1024 * 1024;

export interface ZipEntry {
  name: string;
  size: number;
  /** Unix mode bits of the entry, 0 when the zip was not made on Unix. */
  mode: number;
  encrypted: boolean;
  read(): Buffer;
}

const invalid = (rule: string) => new SetupError('PACKAGE_INVALID', rule);

// zlib.crc32 needs Node 20.15+; the adapter also runs on Node 18 (bundle target).
const CRC_TABLE = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(data: Buffer): number {
  let c = 0xffffffff;
  for (const byte of data) c = CRC_TABLE[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

export function readZip(data: Buffer): ZipEntry[] {
  if (data.length > MAX_ZIP_BYTES) throw invalid('zip-too-large');
  const end = findEnd(data);
  const count = data.readUInt16LE(end + 10);
  if (count > MAX_ENTRIES) throw invalid('too-many-entries');
  let offset = data.readUInt32LE(end + 16);
  const entries: ZipEntry[] = [];
  let total = 0;
  for (let index = 0; index < count; index++) {
    if (offset + 46 > data.length || data.readUInt32LE(offset) !== 0x02014b50) throw invalid('not-zip');
    const madeBy = data.readUInt16LE(offset + 4) >> 8;
    const flags = data.readUInt16LE(offset + 8);
    const method = data.readUInt16LE(offset + 10);
    const crc = data.readUInt32LE(offset + 16);
    const compressed = data.readUInt32LE(offset + 20);
    const size = data.readUInt32LE(offset + 24);
    const nameLength = data.readUInt16LE(offset + 28);
    const skip = nameLength + data.readUInt16LE(offset + 30) + data.readUInt16LE(offset + 32);
    const external = data.readUInt32LE(offset + 38);
    const localOffset = data.readUInt32LE(offset + 42);
    if (compressed === 0xffffffff || size === 0xffffffff || localOffset === 0xffffffff) throw invalid('not-zip');
    const name = data.subarray(offset + 46, offset + 46 + nameLength).toString('utf8');
    total += size;
    if (total > MAX_UNCOMPRESSED) throw invalid('uncompressed-too-large');
    entries.push({
      name, size, mode: madeBy === 3 ? external >>> 16 : 0, encrypted: (flags & 1) === 1,
      read: () => readBody(data, localOffset, method, compressed, size, crc),
    });
    offset += 46 + skip;
  }
  return entries;
}

function findEnd(data: Buffer): number {
  const floor = Math.max(0, data.length - 22 - 0xffff);
  for (let index = data.length - 22; index >= floor; index--) {
    if (data.readUInt32LE(index) === 0x06054b50) return index;
  }
  throw invalid('not-zip');
}

function readBody(data: Buffer, offset: number, method: number, compressed: number, size: number, crc: number): Buffer {
  if (offset + 30 > data.length || data.readUInt32LE(offset) !== 0x04034b50) throw invalid('not-zip');
  const start = offset + 30 + data.readUInt16LE(offset + 26) + data.readUInt16LE(offset + 28);
  if (start + compressed > data.length) throw invalid('not-zip');
  const body = data.subarray(start, start + compressed);
  let out: Buffer;
  if (method === 0) out = Buffer.from(body);
  else if (method === 8) {
    try { out = inflateRawSync(body, { maxOutputLength: Math.max(size, 1) }); } catch { throw invalid('uncompressed-too-large'); }
  } else throw invalid('not-zip');
  if (out.length !== size || crc32(out) !== crc) throw invalid('not-zip');
  return out;
}
