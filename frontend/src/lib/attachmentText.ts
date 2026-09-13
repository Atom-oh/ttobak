import type { Attachment, AttachmentTextLocation, AttachmentTextStatus } from '@/types/meeting';

export const unknownExtraction: AttachmentTextStatus = {
  status: 'unknown', unitCount: 0, complete: false, hasResult: false,
  needsResummary: false, summaryExcerpted: false,
};

export const extractionLabels: Record<AttachmentTextStatus['status'], string> = {
  unknown: '추출 상태 미확인', queued: '추출 대기', running: '텍스트 추출 중',
  succeeded: '추출 완료', partial: '부분 추출', failed: '추출 실패',
};

const extractionErrors: Record<string, string> = {
  INTERRUPTED: '작업이 완료되기 전에 중단되었습니다. 다시 시도할 수 있습니다.',
  PUBLISH_FAILED: '추출 작업을 전달하지 못했습니다. 다시 시도해 주세요.',
  SOURCE_CHANGED: '원본 파일이 변경되었습니다. 다시 추출해 주세요.',
  SOURCE_UNAVAILABLE: '원본 파일을 읽지 못했습니다.',
  INVALID_SOURCE: '첨부 파일의 원본 정보를 확인하지 못했습니다.',
  SOURCE_TOO_LARGE: '텍스트 추출은 20 MiB 이하 파일을 지원합니다.',
  UNSUPPORTED_FORMAT: '텍스트 추출은 PDF, PPTX, DOCX, Markdown만 지원합니다.',
  OCR_REQUIRED: '이미지형 PDF입니다. OCR로 텍스트를 추가한 파일이 필요합니다.',
  NO_EXTRACTABLE_TEXT: '추출할 본문 텍스트를 찾지 못했습니다.',
  INVALID_ENCODING: 'Markdown 파일은 UTF-8 형식이어야 합니다.',
  ENCRYPTED_DOCUMENT: '암호화된 문서는 추출할 수 없습니다.',
  ENCRYPTED_OR_LEGACY_OFFICE: '암호화되었거나 이전 Office 형식인 파일입니다.',
  CORRUPT_DOCUMENT: '문서 구조를 읽지 못했습니다. 원본 파일을 확인해 주세요.',
  UNSAFE_DOCUMENT: '문서에 지원하지 않는 외부 연결 또는 실행 콘텐츠가 있습니다.',
  LIMIT_EXCEEDED: '문서 구조 또는 추출 결과가 처리 한도를 초과했습니다.',
  RESOURCE_LIMIT: '문서 처리에 필요한 자원이 한도를 초과했습니다.',
  TIMEOUT: '텍스트 추출 시간이 초과되었습니다.',
  RESULT_WRITE_FAILED: '추출 결과를 저장하지 못했습니다.',
  WORKER_FAILED: '문서 처리 결과를 확인하지 못했습니다.',
  WORKER_OUTPUT_LIMIT: '추출 결과가 처리 한도를 초과했습니다.',
  PARTIAL_EXTRACTION: '문서의 일부만 추출되었습니다.',
  STATUS_UNAVAILABLE: '저장된 추출 상태를 불러오지 못했습니다.',
};

export function extractionError(code?: string) {
  return code ? extractionErrors[code] ?? '문서 추출을 완료하지 못했습니다.' : undefined;
}

export function unsupportedDocument(attachment: Attachment) {
  if (attachment.textExtraction?.errorCode === 'UNSUPPORTED_FORMAT') return true;
  const source = attachment.originalKey || attachment.name;
  const extension = source.match(/\.([^./]+)$/)?.[1].toLowerCase();
  // Legacy rows can lack a useful filename. Let the server decide on retry.
  return !!extension && !['pdf', 'pptx', 'docx', 'md'].includes(extension);
}

export function documentLocation(location: AttachmentTextLocation) {
  const labels: string[] = [];
  const positive = (value: number | undefined) => typeof value === 'number' && Number.isInteger(value) && value > 0;
  if (location.kind === 'page' && positive(location.page)) labels.push(`문서 ${location.page}쪽`);
  if (location.kind === 'slide' && positive(location.slide)) {
    labels.push(`슬라이드 ${location.slide}`);
    if (location.hidden) labels.push('숨김 슬라이드');
  }
  if (location.kind === 'paragraph' && positive(location.paragraph)) labels.push(`문단 ${location.paragraph}`);
  if (location.kind === 'slide' && positive(location.paragraph)) labels.push(`문단 ${location.paragraph}`);
  if (positive(location.startLine) && positive(location.endLine)) labels.push(`${location.startLine}–${location.endLine}행`);
  if (positive(location.table)) labels.push(`표 ${location.table}`);
  if (positive(location.row)) labels.push(`행 ${location.row}`);
  if (positive(location.cell)) labels.push(`열 ${location.cell}`);
  return labels.join(' · ') || '위치 정보 없음';
}
