// Bounded TOML reader for Codex `config.toml`. It covers the TOML syntax that
// host configuration uses: tables, arrays of tables, dotted and quoted keys,
// all four string forms, arrays, inline tables, booleans and bare scalars.
// Integers, floats and dates are kept as their source text, because intake
// only needs strings, string arrays, string tables and booleans. Errors never
// echo document text, so a secret in a malformed file cannot leak.

export type TomlValue = string | boolean | TomlValue[] | TomlTable;
export interface TomlTable { [key: string]: TomlValue }

const MAX_DEPTH = 32;

class TomlSyntaxError extends Error {
  constructor(line: number) {
    super(`TOML syntax error at line ${line}`);
  }
}

function isTable(value: unknown): value is TomlTable {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

class Parser {
  private pos = 0;
  private readonly root: TomlTable = Object.create(null);
  // Tables created implicitly by a dotted header may still be defined explicitly once.
  private readonly explicit = new Set<TomlTable>();
  private readonly inline = new Set<TomlTable>();

  constructor(private readonly text: string) {}

  parse(): TomlTable {
    let table = this.root;
    while (true) {
      this.skipBlankLines();
      if (this.pos >= this.text.length) return this.root;
      if (this.text[this.pos] === '[') table = this.header();
      else {
        this.keyValue(table, 0);
        this.endOfLine();
      }
    }
  }

  private fail(): never {
    throw new TomlSyntaxError(this.text.slice(0, this.pos).split('\n').length);
  }

  private skipSpaces(): void {
    while (this.text[this.pos] === ' ' || this.text[this.pos] === '\t') this.pos++;
  }

  private skipComment(): void {
    if (this.text[this.pos] !== '#') return;
    while (this.pos < this.text.length && this.text[this.pos] !== '\n') this.pos++;
  }

  private skipBlankLines(): void {
    while (this.pos < this.text.length) {
      this.skipSpaces();
      this.skipComment();
      if (this.text.startsWith('\r\n', this.pos)) this.pos += 2;
      else if (this.text[this.pos] === '\n') this.pos++;
      else return;
    }
  }

  private endOfLine(): void {
    this.skipSpaces();
    this.skipComment();
    if (this.pos >= this.text.length) return;
    if (this.text.startsWith('\r\n', this.pos)) this.pos += 2;
    else if (this.text[this.pos] === '\n') this.pos++;
    else this.fail();
  }

  private header(): TomlTable {
    const array = this.text.startsWith('[[', this.pos);
    this.pos += array ? 2 : 1;
    this.skipSpaces();
    const keys = this.key();
    this.skipSpaces();
    if (!this.text.startsWith(array ? ']]' : ']', this.pos)) this.fail();
    this.pos += array ? 2 : 1;
    this.endOfLine();
    let table = this.root;
    for (const segment of keys.slice(0, -1)) table = this.descend(table, segment);
    const last = keys[keys.length - 1];
    const existing = table[last];
    if (array) {
      const entry: TomlTable = Object.create(null);
      if (existing === undefined) table[last] = [entry];
      else if (Array.isArray(existing)) existing.push(entry);
      else this.fail();
      this.explicit.add(entry);
      return entry;
    }
    if (existing === undefined) {
      const entry: TomlTable = Object.create(null);
      table[last] = entry;
      this.explicit.add(entry);
      return entry;
    }
    if (!isTable(existing) || this.explicit.has(existing) || this.inline.has(existing)) this.fail();
    this.explicit.add(existing);
    return existing;
  }

  // Walks a dotted key, creating implicit tables. The last element of an array
  // of tables is the current table for that name.
  private descend(table: TomlTable, segment: string): TomlTable {
    const existing = table[segment];
    if (existing === undefined) {
      const created: TomlTable = Object.create(null);
      table[segment] = created;
      return created;
    }
    if (Array.isArray(existing)) {
      const last = existing[existing.length - 1];
      if (!isTable(last)) this.fail();
      return last;
    }
    if (!isTable(existing) || this.inline.has(existing)) this.fail();
    return existing;
  }

  private key(): string[] {
    const keys: string[] = [];
    while (true) {
      this.skipSpaces();
      const character = this.text[this.pos];
      if (character === '"') keys.push(this.basicString());
      else if (character === "'") keys.push(this.literalString());
      else {
        const start = this.pos;
        while (/[A-Za-z0-9_-]/.test(this.text[this.pos] ?? '')) this.pos++;
        if (start === this.pos) this.fail();
        keys.push(this.text.slice(start, this.pos));
      }
      this.skipSpaces();
      if (this.text[this.pos] !== '.') return keys;
      this.pos++;
    }
  }

  private keyValue(table: TomlTable, depth: number): void {
    const keys = this.key();
    this.skipSpaces();
    if (this.text[this.pos] !== '=') this.fail();
    this.pos++;
    this.skipSpaces();
    let target = table;
    for (const segment of keys.slice(0, -1)) target = this.descend(target, segment);
    const last = keys[keys.length - 1];
    if (Object.hasOwn(target, last)) this.fail();
    target[last] = this.value(depth);
  }

  private value(depth: number): TomlValue {
    if (depth > MAX_DEPTH) this.fail();
    const character = this.text[this.pos];
    if (this.text.startsWith('"""', this.pos)) return this.multilineBasic();
    if (this.text.startsWith("'''", this.pos)) return this.multilineLiteral();
    if (character === '"') return this.basicString();
    if (character === "'") return this.literalString();
    if (character === '[') return this.array(depth + 1);
    if (character === '{') return this.inlineTable(depth + 1);
    const start = this.pos;
    // Bare scalars: booleans, numbers, dates (a date-time may contain one space).
    while (/[A-Za-z0-9_+.:-]/.test(this.text[this.pos] ?? '') ||
      (this.text[this.pos] === ' ' && /^\d{4}-\d{2}-\d{2}$/.test(this.text.slice(start, this.pos)) &&
        /\d/.test(this.text[this.pos + 1] ?? ''))) this.pos++;
    const token = this.text.slice(start, this.pos);
    if (!token) this.fail();
    if (token === 'true') return true;
    if (token === 'false') return false;
    if (!/^[+-]?[0-9A-Za-z]/.test(token)) this.fail();
    return token;
  }

  private array(depth: number): TomlValue[] {
    this.pos++;
    const values: TomlValue[] = [];
    while (true) {
      this.skipBlankLines();
      if (this.text[this.pos] === ']') {
        this.pos++;
        return values;
      }
      values.push(this.value(depth));
      this.skipBlankLines();
      if (this.text[this.pos] === ',') this.pos++;
      else if (this.text[this.pos] !== ']') this.fail();
    }
  }

  private inlineTable(depth: number): TomlTable {
    this.pos++;
    const table: TomlTable = Object.create(null);
    this.inline.add(table);
    this.skipBlankLines();
    if (this.text[this.pos] === '}') {
      this.pos++;
      return table;
    }
    while (true) {
      this.skipBlankLines();
      this.keyValue(table, depth);
      this.skipBlankLines();
      if (this.text[this.pos] === '}') {
        this.pos++;
        return table;
      }
      if (this.text[this.pos] !== ',') this.fail();
      this.pos++;
      this.skipBlankLines();
      if (this.text[this.pos] === '}') {
        this.pos++;
        return table;
      }
    }
  }

  private escape(): string {
    const character = this.text[this.pos++];
    const simple: Record<string, string> = { b: '\b', t: '\t', n: '\n', f: '\f', r: '\r', e: '\x1b', '"': '"', '\\': '\\' };
    if (character in simple) return simple[character];
    const length = character === 'u' ? 4 : character === 'U' ? 8 : character === 'x' ? 2 : 0;
    if (!length) this.fail();
    const hex = this.text.slice(this.pos, this.pos + length);
    if (!/^[0-9A-Fa-f]+$/.test(hex) || hex.length !== length) this.fail();
    this.pos += length;
    const codePoint = Number.parseInt(hex, 16);
    if (codePoint > 0x10ffff || (codePoint >= 0xd800 && codePoint <= 0xdfff)) this.fail();
    return String.fromCodePoint(codePoint);
  }

  private basicString(): string {
    this.pos++;
    let result = '';
    while (true) {
      const character = this.text[this.pos];
      if (character === undefined || character === '\n') this.fail();
      this.pos++;
      if (character === '"') return result;
      result += character === '\\' ? this.escape() : character;
    }
  }

  private literalString(): string {
    this.pos++;
    const end = this.text.indexOf("'", this.pos);
    const newline = this.text.indexOf('\n', this.pos);
    if (end < 0 || (newline >= 0 && newline < end)) this.fail();
    const value = this.text.slice(this.pos, end);
    this.pos = end + 1;
    return value;
  }

  private multilineBasic(): string {
    this.pos += 3;
    if (this.text.startsWith('\r\n', this.pos)) this.pos += 2;
    else if (this.text[this.pos] === '\n') this.pos++;
    let result = '';
    while (true) {
      if (this.pos >= this.text.length) this.fail();
      if (this.text.startsWith('"""', this.pos)) {
        // Up to two quotes may directly precede the closing delimiter.
        let quotes = 3;
        while (this.text[this.pos + quotes] === '"' && quotes < 5) quotes++;
        result += '"'.repeat(quotes - 3);
        this.pos += quotes;
        return result;
      }
      const character = this.text[this.pos++];
      if (character !== '\\') {
        result += character;
        continue;
      }
      // A line-ending backslash trims the newline and following whitespace.
      const rest = /^[ \t]*\r?\n/.exec(this.text.slice(this.pos, this.pos + 4096));
      if (rest) {
        this.pos += rest[0].length;
        while (/[ \t\r\n]/.test(this.text[this.pos] ?? '')) this.pos++;
        continue;
      }
      result += this.escape();
    }
  }

  private multilineLiteral(): string {
    this.pos += 3;
    if (this.text.startsWith('\r\n', this.pos)) this.pos += 2;
    else if (this.text[this.pos] === '\n') this.pos++;
    const end = this.text.indexOf("'''", this.pos);
    if (end < 0) this.fail();
    let close = end + 3;
    while (this.text[close] === "'" && close - end < 5) close++;
    const value = this.text.slice(this.pos, close - 3);
    this.pos = close;
    return value;
  }
}

export function parseToml(text: string): TomlTable {
  if (text.includes('\0')) throw new TomlSyntaxError(1);
  return new Parser(text.replace(/^﻿/, '')).parse();
}
