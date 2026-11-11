// SA performance report: period keys, the adapter mirror of the server
// validation, and the read-only tool allowlist.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { periodFor, periodBounds, validateReport, validateHighlights, encodedSize, withServerDefaults, REPORT_JSON_SCHEMA }
  from '../dist/report/schema.js';
import { sampleReport } from './fixtures/report-data.mjs';
import { READ_TOOLS, REPORT_SERVERS, WRITE_TOOL_PATTERN, allowedTools, deniedTools } from '../dist/report/tools.js';


test('period_keys_from_dates', () => {
  // ISO weeks at year boundaries.
  assert.deepEqual(periodFor('week', '2026-01-01'), { periodKey: 'W2026-01', start: '2025-12-29', end: '2026-01-04',
    label: '2026년 1주차 (2025-12-29 ~ 2026-01-04)' });
  assert.equal(periodFor('week', '2027-01-01').periodKey, 'W2026-53');
  assert.equal(periodFor('week', '2021-01-03').periodKey, 'W2020-53');
  assert.equal(periodFor('week', '2024-12-30').periodKey, 'W2025-01');
  assert.equal(periodFor('week', '2026-11-11').periodKey, 'W2026-46');
  assert.deepEqual(periodBounds('W2026-53'), { start: '2026-12-28', end: '2027-01-03' });
  assert.throws(() => periodBounds('W2025-53'), { code: 'REPORT_INVALID' });
  // Quarter and year.
  assert.deepEqual(periodFor('quarter', '2026-11-11'), { periodKey: 'Q2026-4', start: '2026-10-01', end: '2026-12-31', label: '2026년 4분기' });
  assert.deepEqual(periodFor('quarter', '2026-02-28'), { periodKey: 'Q2026-1', start: '2026-01-01', end: '2026-03-31', label: '2026년 1분기' });
  assert.deepEqual(periodFor('year', '2026-11-11'), { periodKey: 'Y2026', start: '2026-01-01', end: '2026-12-31', label: '2026년' });
  // Custom: inclusive, at most 400 days.
  assert.deepEqual(periodFor('custom', '2026-11-11', { start: '2026-03-01', end: '2026-06-30' }),
    { periodKey: 'C2026-03-01_2026-06-30', start: '2026-03-01', end: '2026-06-30', label: '2026-03-01 ~ 2026-06-30' });
  assert.equal(periodFor('custom', '2026-11-11', { start: '2026-01-01', end: '2027-02-04' }).periodKey, 'C2026-01-01_2027-02-04');
  assert.throws(() => periodFor('custom', '2026-11-11', { start: '2026-01-01', end: '2027-02-05' }), { code: 'INVALID_ARGUMENT' });
  assert.throws(() => periodFor('custom', '2026-11-11', { start: '2026-06-30', end: '2026-03-01' }), { code: 'INVALID_ARGUMENT' });
  assert.throws(() => periodFor('custom', '2026-11-11', { start: '2026-02-30', end: '2026-03-01' }), { code: 'INVALID_ARGUMENT' });
  assert.throws(() => periodFor('custom', '2026-11-11'), { code: 'INVALID_ARGUMENT' });
  assert.throws(() => periodFor('month', '2026-11-11'), { code: 'INVALID_ARGUMENT' });
  // Retention: a period whose end year + 3 is not in the future is refused before any run.
  assert.throws(() => periodFor('custom', '2026-11-11', { start: '2023-12-01', end: '2023-12-31' }), { code: 'REPORT_PERIOD_EXPIRED' });
  assert.equal(periodFor('custom', '2026-11-11', { start: '2024-01-01', end: '2024-03-31' }).periodKey, 'C2024-01-01_2024-03-31');
  assert.equal(periodFor('custom', '2026-11-11', { start: '2023-12-01', end: '2024-01-31' }).periodKey, 'C2023-12-01_2024-01-31');
  // The UTC year decides: on 2027-01-01 in Seoul it may still be 2026 in UTC.
  assert.equal(periodFor('custom', '2027-01-01', { start: '2024-01-01', end: '2024-12-31' }, 2026).periodKey, 'C2024-01-01_2024-12-31');
  assert.throws(() => periodFor('custom', '2027-01-01', { start: '2024-01-01', end: '2024-12-31' }, 2027), { code: 'REPORT_PERIOD_EXPIRED' });
});

test('valid_report_is_normalized_without_nulls', () => {
  const report = validateReport('Q2026-4', sampleReport());
  assert.deepEqual(report.highlights, undefined);
  assert.deepEqual(report.ledger[0], { label: 'SA Activity', value: '30', unit: '건' });
  // Numeric cells and values become strings; unknown fields are dropped.
  const loose = sampleReport('Q2026-4', { extra: 1,
    ledger: [{ label: 'x', value: 3, unit: '건' }],
    sections: [{ id: 's', title: 't', lede: '', paragraphs: [], tables: [{ columns: ['a'], rows: [[5]] }] }] });
  const cleaned = validateReport('Q2026-4', loose);
  assert.equal(cleaned.extra, undefined);
  assert.equal(cleaned.ledger[0].value, '3');
  assert.deepEqual(cleaned.sections[0].tables[0].rows, [['5']]);
  assert.ok(!JSON.stringify(cleaned).includes('null'));
});

test('markup_rejected_before_upload', () => {
  for (const bad of ['<script>x</script>', '</b>', 'see [x](y)', 'https://acme.slack.com/archives/1', 'SLACK.COM',
    'a\u0000b', 'lone \ud800 surrogate']) {
    assert.throws(() => validateReport('Q2026-4', sampleReport('Q2026-4', { summary: bad })), { code: 'REPORT_INVALID' }, bad);
    assert.throws(() => validateReport('Q2026-4', sampleReport('Q2026-4', {
      sections: [{ id: 's', title: 't', lede: '', paragraphs: [], tables: [{ columns: ['a'], rows: [[bad]] }] }] })),
    { code: 'REPORT_INVALID' });
  }
  // A bare "<" before a digit or space, and Korean, are plain text.
  validateReport('Q2026-4', sampleReport('Q2026-4', { summary: '3 < 5, 한글 <3' }));
});

test('report_caps_mirror_the_server', () => {
  const key = 'Q2026-4';
  const reject = overrides => assert.throws(() => validateReport(key, sampleReport(key, overrides)), { code: 'REPORT_INVALID' });
  reject({ schemaVersion: 2 });
  reject({ periodKey: 'Q2026-3' });
  reject({ period: { start: '2026-10-02', end: '2026-12-31', label: 'x' } });
  reject({ period: { start: '2026-10-01', end: '2026-12-31', label: '' } });
  reject({ generatedAt: 'yesterday' });
  // The server's generatedAt rule: 35 bytes, a 1-9 digit fraction, case-sensitive, a real date.
  reject({ generatedAt: `2026-11-11T03:00:00.${'1'.repeat(200_000)}Z` });
  reject({ generatedAt: '2026-11-11T03:00:00.1234567890Z' });
  reject({ generatedAt: '2026-11-11t03:00:00Z' });
  reject({ generatedAt: '2026-11-11T03:00:00z' });
  reject({ generatedAt: '2026-13-11T03:00:00Z' });
  // Go time.Parse ranges.
  for (const generatedAt of ['2026-02-30T03:00:00Z', '2027-02-29T00:00:00Z', '2026-04-31T00:00:00Z', '2026-11-11T24:00:00Z',
    '2026-11-11T03:60:00Z', '2026-11-11T03:00:60Z', '2026-11-11T03:00:00+24:00', '2026-11-11T03:00:00+09:60', '2026-00-11T03:00:00Z',
    '2026-11-00T03:00:00Z']) reject({ generatedAt });
  assert.equal(validateReport(key, sampleReport(key, { generatedAt: '2028-02-29T00:00:00Z' })).generatedAt, '2028-02-29T00:00:00Z');
  assert.equal(validateReport(key, sampleReport(key, { generatedAt: '2000-02-29T23:59:59-23:59' })).generatedAt, '2000-02-29T23:59:59-23:59');
  for (const generatedAt of ['2026-11-11T03:00:00Z', '2026-11-11T03:00:00.123456789+09:00', new Date('2026-11-11T03:00:00Z').toISOString()]) {
    assert.equal(validateReport(key, sampleReport(key, { generatedAt })).generatedAt, generatedAt);
  }
  reject({ summary: 'x'.repeat(4001) });
  reject({ alias: 'x'.repeat(201) });
  reject({ missingSources: null });
  reject({ sections: Array.from({ length: 41 }, (_, i) => ({ id: `s${i}`, title: 't', lede: '', paragraphs: [] })) });
  reject({ sections: [{ id: 'a', title: 't', lede: '', paragraphs: [] }, { id: 'a', title: 't', lede: '', paragraphs: [] }] });
  reject({ sections: [{ id: 'bad id', title: 't', lede: '', paragraphs: [] }] });
  reject({ sections: [{ id: 'a', title: 't', lede: '', paragraphs: [], tables: [{ columns: ['a', 'b'], rows: [['1']] }] }] });
  reject({ sections: [{ id: 'a', title: 't', lede: '', paragraphs: [], tables: [{ columns: [], rows: [] }] }] });
  reject({ actions: Array.from({ length: 201 }, () => ({ severity: 'lo', title: 't', detail: '', source: '' })) });
  reject({ actions: [{ severity: 'urgent', title: 't', detail: '', source: '' }] });
  reject({ dataBasis: [{ source: '', query: '', exclusions: '' }] });
  reject({ ledger: [{ label: 'a', value: '1', unit: 'x', note: null }] });
  reject({ highlights: { projects: Array.from({ length: 21 }, () => 'p'), initiatives: [], channels: [], programs: [] } });
  // Code points, not UTF-16 units: 4000 emoji fit.
  validateReport(key, sampleReport(key, { summary: '😀'.repeat(4000) }));
  // 256 KiB encoded.
  const big = Array.from({ length: 40 }, (_, i) => ({ id: `s${i}`, title: 't', lede: '', paragraphs: Array.from({ length: 2 }, () => '가'.repeat(4000)) }));
  assert.ok(encodedSize(sampleReport(key, { sections: big })) > 262144);
  reject({ sections: big });
});

test('highlights_file_is_validated', () => {
  assert.deepEqual(validateHighlights({ projects: ['A'], initiatives: [], channels: ['#c'], programs: [] }),
    { projects: ['A'], initiatives: [], channels: ['#c'], programs: [] });
  assert.deepEqual(validateHighlights({ projects: ['A'] }), { projects: ['A'], initiatives: [], channels: [], programs: [] });
  for (const bad of [null, [], { projects: 'A' }, { projects: Array(21).fill('x') }, { projects: ['x'.repeat(201)] },
    { other: [] }, { channels: ['https://x.slack.com'] }]) {
    assert.throws(() => validateHighlights(bad), { code: 'INVALID_ARGUMENT' });
  }
});

test('json_schema_describes_the_model_content', () => {
  assert.equal(REPORT_JSON_SCHEMA.type, 'object');
  assert.equal(REPORT_JSON_SCHEMA.additionalProperties, false);
  for (const field of ['summary', 'ledger', 'sections', 'actions', 'dataBasis', 'partial', 'missingSources']) {
    assert.ok(REPORT_JSON_SCHEMA.required.includes(field), field);
  }
  assert.equal(REPORT_JSON_SCHEMA.properties.sections.maxItems, 40);
  assert.equal(REPORT_JSON_SCHEMA.properties.actions.maxItems, 200);
});

test('allowlist_is_read_only', () => {
  const readDirs = ['/s/skill', '/t/run', '/h/.claude/projects/-t-run'];
  const rules = allowedTools({ skillDir: '/s/skill', outDir: '/t/run/work/out', readDirs, servers: REPORT_SERVERS });
  const writes = /(create|update|add|remove|delete|post|send|inject|upsert|submit|enable_write|edit|archive|cancel|schedule)/;
  assert.equal(String(WRITE_TOOL_PATTERN), String(writes));
  for (const [server, tools] of Object.entries(READ_TOOLS)) {
    assert.ok(REPORT_SERVERS.includes(server));
    for (const tool of tools) assert.doesNotMatch(tool, writes, `${server} ${tool}`);
  }
  for (const rule of rules) {
    const name = rule.split('(')[0];
    assert.doesNotMatch(name, writes, rule);
    assert.ok(!rule.includes('mcp_stdio'), rule);
    assert.ok(!/^mcp__.*\*/.test(rule), `no wildcard MCP rule: ${rule}`);
    if (name === 'Bash') assert.match(rule, /^Bash\(python3 -I \/s\/skill\/scripts\/(calendar_count|extract_tool_json|slack_threads)\.py:\*\)$/);
    // File tools only inside the skill, run and session directories.
    if (['Read', 'Glob', 'Grep'].includes(name)) assert.ok(readDirs.some(dir => rule === `${name}(/${dir}/**)`), rule);
  }
  for (const tool of ['Read', 'Glob', 'Grep', 'Bash', 'Write', 'Edit']) assert.ok(!rules.includes(tool), tool);
  for (const tool of ['Read', 'Glob', 'Grep']) assert.ok(rules.includes(`${tool}(//s/skill/**)`), tool);
  assert.deepEqual(rules.filter(rule => rule.startsWith('Write')), ['Write(//t/run/work/out/**)']);
  // The working directory's .claude/ (a settings source) is denied to every file-writing tool.
  for (const tool of ['Write', 'Edit', 'MultiEdit']) assert.ok(deniedTools('/s/skill', '/t/run/work').includes(`${tool}(//t/run/work/.claude/**)`), tool);
  // The v1.4.0 data-sources read tools.
  for (const tool of ['mcp__aws-sentral-mcp__search_tasks', 'mcp__aws-sentral-mcp__get_team_members', 'mcp__slack-mcp__search',
    'mcp__slack-mcp__lookup_user', 'mcp__aws-tfc-hub-mcp__get_user_info', 'mcp__spec-req-mcp__search_requests_advanced',
    'mcp__aws-outlook-mcp__calendar_view']) assert.ok(rules.includes(tool), tool);
  assert.ok(!rules.includes('mcp__slack-mcp__post_message'));
  assert.ok(!rules.includes('Agent'));
  assert.ok(deniedTools('/s/skill', '/t/run/work').includes('Agent'));
  assert.ok(!rules.some(rule => rule.startsWith('mcp__builder-mcp__')));
  assert.ok(!REPORT_SERVERS.includes('builder-mcp'));
  assert.ok(deniedTools('/s/skill', '/t/run/work').includes('Bash(python3 -I /s/skill/scripts/mcp_stdio.py:*)'));
  // A server that is absent contributes no tools.
  const fewer = allowedTools({ skillDir: '/s/skill', outDir: '/t/run/work/out', readDirs, servers: ['slack-mcp'] });
  assert.ok(!fewer.some(rule => rule.startsWith('mcp__aws-sentral-mcp__')));
});

test('size_is_measured_with_server_defaults', () => {
  const key = 'Q2026-4';
  // Ledger items without notes, sections without tables or cards, data basis without estimates:
  // the server's encoding adds "note":"", "tables":[], "cards":[] and "estimates":"" to each.
  const build = fill => sampleReport(key, {
    ledger: Array.from({ length: 50 }, (_, i) => ({ label: `l${i}`, value: '1', unit: '건' })),
    sections: Array.from({ length: 40 }, (_, i) => ({ id: `s${i}`, title: 't', lede: '',
      paragraphs: i === 0 ? ['a'.repeat(fill)] : Array.from({ length: 2 }, () => 'a'.repeat(3250)) })),
    dataBasis: [{ source: 'SFDC', query: 'q', exclusions: 'e' }],
  });
  const fixed = encodedSize(build(0));
  const fill = 262144 - 100 - fixed;
  assert.ok(fill > 0 && fill <= 4000, String(fill));
  const report = build(fill);
  assert.equal(encodedSize(report), 262144 - 100);
  assert.ok(encodedSize(withServerDefaults(report)) > 262144);
  assert.throws(() => validateReport(key, report), { code: 'REPORT_INVALID' });
});
