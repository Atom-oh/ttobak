import { createHash } from 'node:crypto';

/**
 * The calendar occurrence ID: SHA-256 hex of Go's json.Marshal([]string{sourceId, externalId}).
 * Go escapes <, >, & and the U+2028/U+2029 separators, which JSON.stringify leaves literal.
 */
export function calendarEventId(sourceId: string, externalId: string): string {
  const encoded = JSON.stringify([sourceId, externalId]).replace(/[<>&\u2028\u2029]/g, character =>
    '\\u' + character.charCodeAt(0).toString(16).padStart(4, '0'));
  return createHash('sha256').update(encoded, 'utf8').digest('hex');
}
