// Synthetic stand-in for a local aws-sentral-mcp server. It mirrors only the
// recorded result shapes of three read tools; every ID and name is synthetic.
// FAKE_SENTRAL_MODE: ok | auth (second task page fails) | schema (search_tasks changed).
// FAKE_SENTRAL_REGISTRY: single | mixed (adds a Partner L14 record and an older sales version).
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { CallToolRequestSchema, ListToolsRequestSchema } from '@modelcontextprotocol/sdk/types.js';

const mode = process.env.FAKE_SENTRAL_MODE || 'ok';
const registryMode = process.env.FAKE_SENTRAL_REGISTRY || 'single';
const CANARY = 'SECRET-CANARY-7f3a9c';
const ALIAS = 'testalias';
const OWNER = '005XX0000000001AAA';
const server = new Server({ name: 'fake-sentral', version: '1' }, { capabilities: { tools: {} } });

const object = properties => ({ type: 'object', properties });
server.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [
    { name: 'get_my_personal_details', description: 'synthetic', inputSchema: object({}) },
    { name: 'get_registry_assignments', description: 'synthetic', inputSchema: object({
      employeeIdentifier: { type: 'string' }, hierarchyId: { type: 'string' }, hierarchyLevel: { type: 'string' },
      limit: { type: 'number' }, after: { type: 'string' } }) },
    { name: 'search_tasks', description: 'synthetic', inputSchema: mode === 'schema'
      ? object({ query: { type: 'string' }, limit: { type: 'number' } })
      : object({ condition: { type: 'object' }, limit: { type: 'number' }, after: { type: 'string' } }) },
  ],
}));

function task(id, extra) {
  return {
    id, subject: `Synthetic task ${id}`, status: 'Completed', type: 'Call', isClosed: true,
    activityDate: '2026-10-27', createdDate: '2026-09-17T12:55:23.000Z',
    ownerId: OWNER, owner: { name: 'Test User', alias: ALIAS },
    accountId: '001XX000000002AAA', account: { id: '001XX000000002AAA', name: 'Example Two' },
    whatId: '006XX0000000001AAA', what: { __typename: 'SFDC_Opportunity', name: 'Example opp' },
    sa_Activity__c: 'Other Workshops [Workshops]', sa_Activity_Category__c: 'Workshops',
    sa_Activity_Date__c: '2026-10-27', bd_Activity_Type__c: 'SA - MGMT - Meeting / Office Hours',
    has_Related_Opportunity__c: true,
    // Never in the real list result; present here to prove the parser drops them.
    description: `notes ${CANARY}`, comments: `comment ${CANARY}`,
    ...extra,
  };
}

const PAGES = [
  [task('00TXX0000000001AAA'),
    task('00TXX0000000002AAA', { accountId: null, account: null, sa_Activity_Category__c: null, type: 'Email' })],
  [task('00TXX0000000003AAA', { accountId: '001XX000000001AAA',
    account: { id: '001XX000000001AAA', name: 'Activity name ignored' }, sa_Activity_Date__c: null, activityDate: '2026-03-02' }),
  task('00TXX0000000004AAA', { sa_Activity_Date__c: '2024-05-01', activityDate: '2024-05-01' })],
];

const wrap = data => ({ content: [{ type: 'text', text: JSON.stringify({ data }) }] });
const fail = text => ({ isError: true, content: [{ type: 'text', text }] });

server.setRequestHandler(CallToolRequestSchema, async request => {
  const args = request.params.arguments ?? {};
  switch (request.params.name) {
    case 'get_my_personal_details':
      return wrap({ alias: ALIAS, sfdcId: OWNER, directReports: [], email: `${CANARY}@example.invalid` });
    case 'get_registry_assignments':
      // hierarchyId is optional and must not be sent: versions are not years.
      if (args.employeeIdentifier !== ALIAS || args.hierarchyLevel !== 'L14' || args.limit !== 500 ||
          'hierarchyId' in args) return fail('unexpected registry arguments');
      const records = [{ id: 'guid:r:001XX000000001AAA', hierarchyLevel: 'L14', hierarchyId: 'sales/2026-01-01',
        assignmentSource: 'REGISTRY', nodeId: '001XX000000001AAA', nodeName: 'Example Corp.',
        parentNode: { area: 'Korea', awsentralTerritory: { name: 'NAMED-EXAMPLE' } } }];
      if (registryMode === 'mixed') {
        records.push(
          { id: 'guid:r:PS:SELLER.0001', hierarchyLevel: 'L14', hierarchyId: 'partner/2026-01-01',
            assignmentSource: 'REGISTRY', nodeId: 'PS:SELLER.0001', nodeName: 'Partner seller' },
          { id: 'guid:r:PSXX0000000002AAA', hierarchyLevel: 'L14', hierarchyId: 'partner/2026-01-01',
            assignmentSource: 'REGISTRY', nodeId: 'PSXX0000000002AAA', nodeName: 'Partner seller two' },
          { id: 'guid:r:001XX0000000OLDAAA', hierarchyLevel: 'L14', hierarchyId: 'sales/2025-01-01',
            assignmentSource: 'REGISTRY', nodeId: '001XX0000000OLDAAA', nodeName: 'Old version' });
      }
      return wrap({ resultRecords: records, totalCount: String(records.length), hasNextPage: false, cursor: null });
    case 'search_tasks': {
      const conditions = args.condition?.conditions ?? [];
      const owner = conditions.find(item => item.field === 'ownerId');
      const since = conditions.find(item => item.field === 'activityDate' && item.operator === 'GTE');
      const until = conditions.find(item => item.field === 'activityDate' && item.operator === 'LTE');
      if (args.condition?.operator !== 'AND' || conditions.length !== 3 || owner?.value !== OWNER ||
          owner?.operator !== 'EXACT_MATCH' || !/^\d{4}-01-01$/.test(String(since?.value)) ||
          !/^\d{4}-\d{2}-\d{2}$/.test(String(until?.value)) || args.limit !== 500) {
        return fail('unexpected task arguments');
      }
      const index = args.after === undefined ? 0 : args.after === 'page-2' ? 1 : -1;
      if (index < 0) return fail('unknown cursor');
      if (index === 1 && mode === 'auth') {
        process.stderr.write('Midway cookie expired; run mwinit\n');
        return fail('Authentication failed: run mwinit -o');
      }
      return wrap({ resultRecords: PAGES[index], totalCount: '4', hasNextPage: index === 0,
        cursor: index === 0 ? 'page-2' : null });
    }
    default:
      return fail('unknown tool');
  }
});

await server.connect(new StdioServerTransport());
