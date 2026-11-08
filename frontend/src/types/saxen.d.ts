declare module 'saxen' {
  export class Parser {
    on(event: 'openTag', listener: (name: string, attributes: () => Record<string, string>, decode: (value: string) => string) => void): void;
    on(event: 'closeTag', listener: (name: string) => void): void;
    on(event: 'error', listener: (error: Error) => void): void;
    write(value: string): void;
    end(): void;
  }
}
