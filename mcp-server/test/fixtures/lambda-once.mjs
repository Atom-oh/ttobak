// Invokes the handler once and then does nothing: the process must exit on its
// own, proving no listening or pooled socket keeps the event loop alive.
const { handler } = await import(process.env.TTOBAK_LAMBDA_ENTRY);
const result = await handler(JSON.parse(process.env.TTOBAK_LAMBDA_EVENT), {});
process.stdout.write(JSON.stringify(result));
