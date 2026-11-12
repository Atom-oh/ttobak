// Canonical SFDC record IDs, mirroring the server (`model.CanonicalSfdcID`).
// A 15-character ID gains its 3-character case-checksum suffix; an 18-character
// ID is kept only when its suffix matches. Every other value is invalid.

const ALPHANUMERIC = /^[0-9A-Za-z]+$/;
const SUFFIX_ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ012345';

function suffix(base: string): string {
  let result = '';
  for (let chunk = 0; chunk < 3; chunk++) {
    let bits = 0;
    for (let index = 0; index < 5; index++) {
      const character = base[chunk * 5 + index];
      if (character >= 'A' && character <= 'Z') bits |= 1 << index;
    }
    result += SUFFIX_ALPHABET[bits];
  }
  return result;
}

/** The 18-character form of a 15- or 18-character SFDC ID, or null when it is not one. */
export function canonicalSfdcId(id: unknown): string | null {
  if (typeof id !== 'string' || (id.length !== 15 && id.length !== 18) || !ALPHANUMERIC.test(id)) return null;
  const expected = suffix(id.slice(0, 15));
  if (id.length === 15) return id + expected;
  return id.slice(15) === expected ? id : null;
}
