import { MAX_READING_BYTES } from './reading.js';

export type ProjectSection = 'detail' | 'brief' | 'metadata' | 'members' | 'meetings' | 'research' | 'insights';
export interface ProjectSectionOptions {
  projectId: string;
  section: ProjectSection;
  pageSize: number;
  cursor?: string;
  from?: string;
  to?: string;
  types?: string[];
}

const insightTypes = ['trend', 'need', 'competitive', 'risk', 'opportunity', 'tech', 'stakeholder', 'action'];
const phases: Record<ProjectSection, string[]> = {
  detail: ['metadata', 'members'], brief: ['metadata', 'members', 'meetings', 'research', 'insights'],
  metadata: ['metadata'], members: ['members'], meetings: ['meetings'], research: ['research'], insights: ['insights'],
};

export function projectSectionOptions(args: Record<string, unknown>, fallback: ProjectSection): ProjectSectionOptions {
  if (Object.keys(args).some(key => !['projectId', 'section', 'pageSize', 'cursor', 'from', 'to', 'types'].includes(key)) ||
      typeof args.projectId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(args.projectId)) {
    throw new Error('INVALID_ARGUMENT: invalid project reading identity/options');
  }
  const section = args.section === undefined ? fallback : args.section;
  const allowed = fallback === 'brief' ? ['brief', 'metadata', 'members', 'meetings', 'research', 'insights'] :
    fallback === 'detail' ? ['detail', 'metadata', 'members'] : [fallback];
  if (typeof section !== 'string' || !allowed.includes(section)) throw new Error('INVALID_ARGUMENT: unsupported project section');
  const pageSize = args.pageSize === undefined ? 4000 : args.pageSize;
  if (typeof pageSize !== 'number' || !Number.isInteger(pageSize) || pageSize < 1 || pageSize > 8000) {
    throw new Error('INVALID_ARGUMENT: pageSize must be 1–8000 Unicode code points');
  }
  if (args.cursor !== undefined && (typeof args.cursor !== 'string' || !args.cursor.length || args.cursor.length > 2048)) {
    throw new Error('INVALID_CURSOR: invalid project continuation');
  }
  for (const name of ['from', 'to']) {
    const value = args[name];
    if (value !== undefined && (typeof value !== 'string' || value.length > 64 ||
        !/^\d{4}-\d\d-\d\dT/.test(value) || !Number.isFinite(Date.parse(value)))) {
      throw new Error(`INVALID_ARGUMENT: ${name} must be RFC3339`);
    }
  }
  if (args.types !== undefined && (!Array.isArray(args.types) || args.types.length > 8 ||
      args.types.some(value => typeof value !== 'string' || !insightTypes.includes(value)))) {
    throw new Error('INVALID_ARGUMENT: invalid insight types');
  }
  if (section !== 'brief' && section !== 'insights' && ['from', 'to', 'types'].some(key => args[key] !== undefined)) {
    throw new Error('INVALID_ARGUMENT: insight filters require brief or insights');
  }
  return { projectId: args.projectId, section: section as ProjectSection, pageSize,
    cursor: args.cursor as string | undefined, from: args.from as string | undefined,
    to: args.to as string | undefined, types: args.types as string[] | undefined };
}

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

export function projectSectionResult(value: unknown, options: ProjectSectionOptions) {
  if (record(value) && value.authorizationPending === true) {
    if (value.kind !== 'project' || value.projectId !== options.projectId || value.section !== options.section ||
        value.phase !== 'authorization' || !record(value.page) || value.page.complete !== false ||
        Object.keys(value.page).some(key => !['complete', 'nextCursor'].includes(key)) ||
        typeof value.page.nextCursor !== 'string' || !value.page.nextCursor.length || value.page.nextCursor.length > 2048 ||
        Object.keys(value).some(key => !['kind', 'projectId', 'section', 'phase', 'authorizationPending', 'page'].includes(key))) {
      throw new Error('INVALID_READING_RESPONSE: invalid authorization continuation');
    }
    return { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  }
  if (!record(value) || value.kind !== 'project' || value.projectId !== options.projectId || value.consistency !== 'current_group' ||
      value.section !== options.section || !phases[options.section].includes(value.phase as string) ||
      typeof value.revision !== 'string' || !/^[a-f0-9]{64}$/.test(value.revision) ||
      typeof value.dataJson !== 'string') throw new Error('INVALID_READING_RESPONSE: invalid project source');
  const fragment = value.fragment;
  if (!record(fragment) || fragment.unit !== 'unicode_code_points' || typeof fragment.complete !== 'boolean' ||
      ![fragment.startOffset, fragment.endOffset, fragment.totalCodePoints].every(n => typeof n === 'number' && Number.isSafeInteger(n) && n >= 0) ||
      (fragment.endOffset as number) <= (fragment.startOffset as number) ||
      (fragment.endOffset as number) > (fragment.totalCodePoints as number) ||
      Array.from(value.dataJson).length !== (fragment.endOffset as number) - (fragment.startOffset as number) ||
      (fragment.complete ? fragment.endOffset !== fragment.totalCodePoints :
        (fragment.endOffset as number) >= (fragment.totalCodePoints as number))) {
    throw new Error('INVALID_READING_RESPONSE: invalid project JSON fragment');
  }
  if (fragment.startOffset === 0 && fragment.complete) {
    try {
      const parsed = JSON.parse(value.dataJson);
      if (value.phase === 'metadata' ? !record(parsed) : !Array.isArray(parsed)) throw new Error('wrong JSON shape');
    } catch { throw new Error('INVALID_READING_RESPONSE: invalid completed project JSON group'); }
  }
  const page = value.page;
  if (!record(page) || typeof page.complete !== 'boolean' || (page.complete &&
      (!fragment.complete || value.phase !== phases[options.section].at(-1))) ||
      (page.complete ? page.nextCursor !== null :
        typeof page.nextCursor !== 'string' || !page.nextCursor.length || page.nextCursor.length > 2048)) {
    throw new Error('INVALID_READING_RESPONSE: invalid project continuation');
  }
  const result = { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(result)) > MAX_READING_BYTES) throw new Error('READING_LIMIT: project page exceeds 32000 bytes');
  return result;
}
