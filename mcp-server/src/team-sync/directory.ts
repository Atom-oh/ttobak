// The internal employee directory, read through the local `builder-mcp` server.
// One allowlisted tool; only the alias, the manager flag and the manager alias
// leave this module. Every other directory field is dropped at parse time.

import { IntakeError } from '../intake/errors.js';
import { parsePathList, resolveCommand } from '../intake/hosts.js';
import { AUTH_PATTERN, McpSession, ToolResultError } from '../intake/mcp-client.js';
import type { IntakeEnvironment } from '../intake/run.js';
import { TEAM_PATTERN, type DirectoryPerson } from './rule.js';

export const DIRECTORY_TOOL = 'ReadInternalWebsites';
export const DIRECTORY_URL_PREFIX = 'https://phonetool.amazon.com/users/';
const CALL_TIMEOUT_MS = 30_000;
const BUILDER_COMMAND = 'builder-mcp';

export interface Directory {
  lookup(alias: string): Promise<DirectoryPerson | null>;
  close(): Promise<void>;
}

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function login(value: unknown): string | null {
  return typeof value === 'string' && TEAM_PATTERN.test(value.toLowerCase()) ? value.toLowerCase() : null;
}

/**
 * Reads `content.status`, `content.content.login`, `is_manager` and
 * `manager.login` of the directory tool's JSON text. A missing person, a
 * non-success status or an unexpected shape is null; an authentication failure
 * is an error. Logins are lowercased.
 */
export function parsePerson(_alias: string, toolText: string): DirectoryPerson | null {
  let value: unknown;
  try { value = JSON.parse(toolText); } catch {
    if (AUTH_PATTERN.test(toolText)) throw new IntakeError('AUTH_EXPIRED_MIDWAY', 'the directory reported an authentication failure');
    return null;
  }
  const outer = record(value) ? value.content : undefined;
  if (!record(outer)) return null;
  if (outer.status !== 'success') {
    if (AUTH_PATTERN.test(toolText)) {
      throw new IntakeError('AUTH_EXPIRED_MIDWAY', 'the directory reported an authentication failure');
    }
    return null;
  }
  const person = outer.content;
  if (!record(person)) return null;
  const own = login(person.login);
  if (own === null || typeof person.is_manager !== 'boolean') return null;
  const manager = record(person.manager) ? login(person.manager.login) : null;
  return { login: own, isManager: person.is_manager, managerLogin: manager };
}

export async function openDirectory(environment: IntakeEnvironment): Promise<Directory> {
  const pathDirs = parsePathList(environment.env.PATH);
  const command = resolveCommand(BUILDER_COMMAND, pathDirs, environment.home);
  if (!command) throw new IntakeError('DIRECTORY_UNAVAILABLE', `${BUILDER_COMMAND} was not found on PATH`);
  let session;
  try {
    session = await McpSession.open({ command, args: [], env: {} }, BUILDER_COMMAND,
      { pathDirs, allowedTools: [DIRECTORY_TOOL], callTimeoutMs: CALL_TIMEOUT_MS });
  } catch (error) {
    if (error instanceof IntakeError && error.code === 'SERVER_START_FAILED') {
      throw new IntakeError('DIRECTORY_UNAVAILABLE', `${BUILDER_COMMAND} did not start`);
    }
    throw error;
  }
  return {
    async lookup(alias) {
      let result;
      try {
        result = await session.call(DIRECTORY_TOOL, { inputs: [`${DIRECTORY_URL_PREFIX}${alias}`] });
      } catch (error) {
        // The directory answered this alias with a non-auth error: no proposal for it.
        if (error instanceof ToolResultError) return null;
        if (error instanceof IntakeError && error.code !== 'AUTH_EXPIRED_MIDWAY') {
          throw new IntakeError('DIRECTORY_UNAVAILABLE', `the directory call failed (${error.code})`);
        }
        throw error;
      }
      const text = (result.content ?? []).map(item => item.type === 'text' ? item.text : '').join('\n');
      return parsePerson(alias, text);
    },
    close: () => session.close(),
  };
}
