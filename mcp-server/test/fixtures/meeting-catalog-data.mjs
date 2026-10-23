export function meetingCatalogPage(extra = {}) {
  return { kind: 'meeting_catalog', phase: 'team', consistency: 'current_group', revision: 'a'.repeat(64),
    dataJson: '[]', fragment: { unit: 'unicode_code_points', startOffset: 0, endOffset: 2, totalCodePoints: 2, complete: true },
    overlapPossible: true, unionKey: 'meetingId', page: { complete: true, nextCursor: null }, ...extra };
}
