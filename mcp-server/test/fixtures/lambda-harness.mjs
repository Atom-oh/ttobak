// Runs the Lambda handler in its own process (so the synthetic-JWKS preload
// applies) and answers one JSON event per stdin line with one JSON result.
import { createInterface } from 'node:readline';

const { handler } = await import(process.env.TTOBAK_LAMBDA_ENTRY);
for await (const line of createInterface({ input: process.stdin })) {
  if (!line.trim()) continue;
  const context = { callbackWaitsForEmptyEventLoop: true };
  const result = await handler(JSON.parse(line), context);
  process.stdout.write(JSON.stringify({ ...result, _context: context }) + '\n');
}
