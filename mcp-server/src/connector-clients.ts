/** Deployment-owned identities. A partial cutover must fail closed. */
export function connectorClientIds(env: NodeJS.ProcessEnv): readonly string[] | undefined {
  if (env.TTOBAK_CONNECTOR_PRIVATE_BRIDGE === undefined || env.TTOBAK_CONNECTOR_PRIVATE_BRIDGE === 'false') {
    return undefined;
  }
  if (env.TTOBAK_CONNECTOR_PRIVATE_BRIDGE !== 'true') throw new Error('INVALID_CONNECTOR_ROLLOUT');
  const ids = [env.CONNECTOR_READ_CLIENT_ID, env.CONNECTOR_WRITE_CLIENT_ID];
  if (ids.some(id => !id || !/^[A-Za-z0-9]{1,128}$/.test(id)) || ids[0] === ids[1] ||
      ids.includes(env.TTOBAK_CLIENT_ID)) throw new Error('INVALID_CONNECTOR_CLIENT_IDS');
  return Object.freeze(ids as string[]);
}
