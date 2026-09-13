import type { QASourceDetail } from '@/types/meeting';

const identifier = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;
const internalKey = /^(?:s3:\/\/|(?:USER|ACCOUNT|MEETING|DOC|ATTACH|ATTEXT)#|(?:canonical|files|transcripts|docs|docs-pdf|images|audio)\/)/i;
const kindLabels: Record<string, string> = {
  meeting: '회의록', personalDocument: '개인 문서', accountDocument: '계정 문서',
  meetingAttachment: '첨부 문서', legacyText: '참고 문서',
};
function id(value: string | undefined): value is string { return typeof value === 'string' && identifier.test(value); }
function partition(value: string | undefined, prefix: string) {
  const result = typeof value === 'string' && value.startsWith(`${prefix}#`) ? value.slice(prefix.length + 1) : '';
  return id(result) ? result : undefined;
}
function appLink(source: QASourceDetail): string | undefined {
  const resource = source.resourceId;
  if (source.resourceKind === 'meeting' && id(resource) && source.sourceSK === `MEETING#${resource}` && partition(source.sourcePK, 'USER')) {
    return `/meeting/${encodeURIComponent(resource)}`;
  }
  if (source.resourceKind === 'personalDocument' && id(resource) && source.sourceSK === `DOC#${resource}` && partition(source.sourcePK, 'USER')) {
    return `/docs/${encodeURIComponent(resource)}`;
  }
  if (source.resourceKind === 'accountDocument' && id(resource) && source.sourceSK === `DOC#${resource}`) {
    const account = partition(source.sourcePK, 'ACCOUNT');
    if (account) return `/accounts/${encodeURIComponent(account)}/docs/${encodeURIComponent(resource)}`;
  }
  if (source.resourceKind === 'meetingAttachment') {
    if (source.attachmentId && resource && source.attachmentId !== resource) return;
    const meeting = source.meetingId ?? partition(source.sourceSK, 'MEETING');
    const attachment = source.attachmentId ?? resource;
    if (id(meeting) && id(attachment) && source.sourceSK === `MEETING#${meeting}` && partition(source.sourcePK, 'USER')) {
      return `/meeting/${encodeURIComponent(meeting)}#attachment-${encodeURIComponent(attachment)}`;
    }
  }
}
function externalLink(value: string | undefined) {
  if (!value) return;
  try {
    const url = new URL(value);
    if ((url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password) return url;
  } catch { /* A malformed or internal URI is a plain source label. */ }
}
export interface DisplayQASource {
  label: string;
  href?: string;
  external: boolean;
  kind?: string;
  caveats: string[];
}
export function qaSources(sources: string[] = [], details: QASourceDetail[] = []): DisplayQASource[] {
  const validDetails = (Array.isArray(details) ? details : []).filter((detail) => detail && typeof detail === 'object');
  const covered = new Set(validDetails.map((detail) => detail.uri).filter(Boolean));
  const displayed = validDetails.map((detail): DisplayQASource => {
    const knownKind = Object.prototype.hasOwnProperty.call(kindLabels, detail.resourceKind) ? kindLabels[detail.resourceKind] : undefined;
    const kind = knownKind ?? '참고 자료';
    const title = typeof detail.title === 'string' ? detail.title.trim() : '';
    const href = appLink(detail);
    // Canonical resources never fall through to a raw storage/external URI.
    const external = !knownKind ? externalLink(detail.uri) : undefined;
    return {
      label: title && !internalKey.test(title) ? title : kind,
      href: href ?? external?.href, external: !!external, kind,
      caveats: [
        ...(detail.usingPreviousResult ? ['이전 추출 결과'] : []),
        ...(detail.partial ? ['부분 근거'] : []),
        ...(detail.filePending ? ['파일 처리 대기'] : []),
      ],
    };
  });
  for (const source of Array.isArray(sources) ? sources : []) {
    if (typeof source !== 'string' || covered.has(source)) continue;
    const external = externalLink(source);
    displayed.push({
      label: external?.hostname || (!internalKey.test(source) && !/^[a-z][a-z0-9+.-]*:[^\s]/i.test(source) ? source : '참고 자료'),
      href: external?.href, external: !!external, caveats: [],
    });
  }
  return displayed;
}
