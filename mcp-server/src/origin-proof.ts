import { timingSafeEqual } from 'node:crypto';
import { SecretsManagerClient, GetSecretValueCommand } from '@aws-sdk/client-secrets-manager';

interface ProofSet {
  schemaVersion: number;
  current: string;
  previous: string;
  rotationStartedAt: number;
  previousExpiresAt: number;
  generation: string;
  phase: 'seeded' | 'rotating' | 'active';
}

export const validProof = (value: unknown): value is string =>
  typeof value === 'string' && value.length >= 16 && value.length <= 128 &&
  /^[\x21-\x2b\x2d-\x7e]+$/.test(value); // Commas signal combined duplicate headers.

export function parseProofs(raw: unknown): ProofSet | undefined {
  if (typeof raw !== 'string' || Buffer.byteLength(raw) > 2048) return undefined;
  let value: Partial<ProofSet>;
  try { value = JSON.parse(raw); } catch { return undefined; }
  if (!value || typeof value !== 'object' ||
      !['schemaVersion', 'current', 'previous', 'rotationStartedAt', 'previousExpiresAt', 'generation', 'phase']
        .every(field => Object.hasOwn(value, field)) ||
      value.schemaVersion !== 1 || !validProof(value.current) ||
      typeof value.generation !== 'string' || !/^[0-9a-f]{32}$/.test(value.generation) ||
      !Number.isSafeInteger(value.rotationStartedAt) || !Number.isSafeInteger(value.previousExpiresAt) ||
      value.rotationStartedAt! < 0) return undefined;
  if (value.phase === 'seeded' || value.phase === 'active') {
    return value.previous === '' && value.previousExpiresAt === 0 ? value as ProofSet : undefined;
  }
  if (value.phase !== 'rotating' || !validProof(value.previous) || value.previous === value.current ||
      value.rotationStartedAt! <= 0 || value.previousExpiresAt! <= value.rotationStartedAt! ||
      value.previousExpiresAt! - value.rotationStartedAt! > 24 * 60 * 60) return undefined;
  return value as ProofSet;
}

function equals(actual: string, expected: string): boolean {
  const left = Buffer.from(actual), right = Buffer.from(expected);
  return left.length === right.length && timingSafeEqual(left, right);
}

type ReadSecret = (arn: string, signal: AbortSignal) => Promise<unknown>;

export class OriginVerifier {
  private cached?: { value: ProofSet; expires: number };
  private pending?: Promise<ProofSet | undefined>;

  constructor(private readonly arn: string, private readonly read: ReadSecret,
              private readonly legacy = '', private readonly now = () => Date.now() / 1000,
              private readonly lookupMs = 3000) {}

  private async secret(): Promise<ProofSet | undefined> {
    if (this.cached && this.now() < this.cached.expires) return this.cached.value;
    if (this.pending) return this.pending;
    this.pending = (async () => {
      const abort = new AbortController();
      let timer: ReturnType<typeof setTimeout> | undefined;
      try {
        const raw = await Promise.race([
          this.read(this.arn, abort.signal),
          new Promise<undefined>((resolve) => {
            timer = setTimeout(() => { abort.abort(); resolve(undefined); }, this.lookupMs);
          }),
        ]);
        const value = parseProofs(raw);
        if (value) this.cached = { value, expires: this.now() + 60 };
        return value;
      } catch {
        return undefined; // No service message, proof, header or ARN enters a log.
      } finally {
        if (timer) clearTimeout(timer);
      }
    })().finally(() => { this.pending = undefined; });
    return this.pending;
  }

  async verify(headers: Record<string, unknown>): Promise<boolean> {
    const values = Object.entries(headers).filter(([name]) => name.toLowerCase() === 'x-origin-verify');
    if (values.length !== 1 || !validProof(values[0][1])) return false;
    const actual = values[0][1];
    // Temporary rollout compatibility; an ARN never falls back to legacy.
    if (!this.arn) return validProof(this.legacy) && equals(actual, this.legacy);
    const value = await this.secret();
    if (!value) return false;
    const current = equals(actual, value.current), previous = equals(actual, value.previous);
    return current || (previous && value.phase === 'rotating' &&
      this.now() >= value.rotationStartedAt && this.now() < value.previousExpiresAt);
  }
}

export function environmentOriginVerifier(env: NodeJS.ProcessEnv): OriginVerifier {
  const client = new SecretsManagerClient({ maxAttempts: 1 });
  return new OriginVerifier(env.ORIGIN_VERIFY_SECRET_ARN ?? '', async (arn, signal) => {
    const result = await client.send(new GetSecretValueCommand({ SecretId: arn }), { abortSignal: signal });
    return result.SecretString;
  }, env.ORIGIN_VERIFY_SECRET ?? '');
}
