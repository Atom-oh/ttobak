// A valid SA performance report for the report tests.
import { periodBounds } from '../../dist/report/schema.js';

export function sampleReport(key = 'Q2026-4', overrides = {}) {
  const { start, end } = periodBounds(key);
  return {
    schemaVersion: 1, periodKey: key, period: { start, end, label: '2026년 4분기' },
    alias: 'alice', generatedAt: '2026-11-11T00:00:00.000Z', model: 'claude-opus-5-5', partial: false,
    summary: '고객 대응 12건, SA Activity 30건.',
    ledger: [{ label: 'SA Activity', value: '30', unit: '건' }],
    sections: [{ id: 'customers', title: '고객 대응', lede: '핵심', paragraphs: ['문단'],
      tables: [{ columns: ['고객', '건수'], rows: [['Acme', '3']] }], cards: [{ title: '카드', body: '본문' }] }],
    actions: [{ severity: 'hi', title: '로깅 누락', detail: '3건', source: 'SFDC' }],
    dataBasis: [{ source: 'SFDC', query: 'ownerId', exclusions: '없음' }],
    ...overrides,
  };
}
