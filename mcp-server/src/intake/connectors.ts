// Tool allowlists for the task runner's provider connectors. Each connector is
// a separately selected local MCP server; only the tools below are ever probed
// or called, and the session refuses any other name before sending it.

import type { ToolExpectation } from './mcp-client.js';

export type ConnectorId = 'calendar' | 'sfdc' | 'meetings' | 'specreq' | 'slack' | 'links';
/** Calendar, SFDC and links are sync connectors with their own run commands, not task-runner providers. */
export type TaskConnectorId = Exclude<ConnectorId, 'calendar' | 'sfdc' | 'links'>;

export const TASK_CONNECTORS: readonly TaskConnectorId[] = ['meetings', 'specreq', 'slack'];

export const CONNECTOR_TOOLS: Record<TaskConnectorId, ToolExpectation[]> = {
  meetings: [
    { name: 'list_meetings', properties: ['startTime', 'endTime', 'userEmail'] },
    { name: 'get_meeting', properties: ['meetingId', 'getMaster', 'userEmail'] },
  ],
  specreq: [
    { name: 'check_user_is_tam', properties: [] },
    { name: 'search_requests_advanced', properties: ['resolverId', 'requesterId', 'stateId', 'au_ca_gte', 'pageSize',
      'fields_json', 'lastEvaluatedIndex_json'] },
    { name: 'get_request', properties: ['request_id'] },
    { name: 'acknowledge_request', properties: ['request_id'] },
  ],
  slack: [
    { name: 'lookup_user', properties: ['query'] },
    { name: 'open_dm_channel', properties: ['userIds'] },
    { name: 'list_drafts', properties: [] },
    { name: 'post_draft', properties: ['channel', 'text'] },
  ],
};

/**
 * Read tools of the `links` sync connector, which reuses the Slack server
 * selection with its own session. No write or search tool is ever allowed.
 */
export const LINKS_TOOLS: ToolExpectation[] = [
  { name: 'list_my_channels', properties: [] },
  { name: 'get_messages', properties: ['channel', 'since', 'until', 'limit', 'cursor', 'includeThreadReplies'] },
];

export function isTaskConnector(value: unknown): value is TaskConnectorId {
  return typeof value === 'string' && (TASK_CONNECTORS as readonly string[]).includes(value);
}

export function allowedToolNames(connector: TaskConnectorId): string[] {
  return CONNECTOR_TOOLS[connector].map(tool => tool.name);
}
