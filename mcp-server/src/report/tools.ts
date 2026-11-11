// Read-only tool allowlist of a report run. The run uses
// `--permission-mode dontAsk`, so every tool not listed here is denied by the
// Claude CLI itself; the prompt is not the boundary. MCP names are the read
// tools that references/data-sources.md of sa-performance-report v1.4.0
// (internal-mcp-setup, 2026-11-07) calls. No server wildcard, no general Bash,
// file tools only inside the skill and run directories, and no `mcp_stdio.py`
// (it could start any server and call any tool).

/**
 * Servers copied from the user's Claude config, when present. builder-mcp is
 * left out: its only use in the skill is `ReadInternalWebsites` for blog
 * drafts, a fetch of model-chosen URLs that could carry data out.
 */
export const REPORT_SERVERS = ['aws-sentral-mcp', 'aws-outlook-mcp', 'slack-mcp', 'spec-req-mcp', 'aws-tfc-hub-mcp'] as const;
export type ReportServer = typeof REPORT_SERVERS[number];

export const READ_TOOLS: Record<ReportServer, readonly string[]> = {
  'aws-sentral-mcp': ['get_my_personal_details', 'search_users', 'search_tasks', 'search_opportunities', 'get_team_members'],
  'aws-outlook-mcp': ['calendar_view'],
  'slack-mcp': ['lookup_user', 'search', 'get_channel'],
  'spec-req-mcp': ['get_date_timestamp', 'search_requests_advanced'],
  'aws-tfc-hub-mcp': ['get_user_info', 'get_activities', 'get_progress', 'get_competency_by_id'],
};

/** Skill scripts the run may execute, always as `python3 -I <skill>/scripts/<name>`. */
export const SKILL_SCRIPTS = ['calendar_count.py', 'extract_tool_json.py', 'slack_threads.py'] as const;

/** Any tool name of this shape is a write and is never allowed. */
export const WRITE_TOOL_PATTERN = /(create|update|add|remove|delete|post|send|inject|upsert|submit|enable_write|edit|archive|cancel|schedule)/;

/**
 * Denied explicitly as well, in case a future default allows them. The
 * working directory is the CLI's project settings root
 * (`--setting-sources project,local`), so no file tool may touch its
 * `.claude/` folder: a planted `settings.local.json` could allow write tools.
 */
export function deniedTools(skillDir: string, workDir: string): string[] {
  return ['Agent', 'WebFetch', 'WebSearch', 'NotebookEdit', 'mcp__aws-outlook-mcp__enable_write_tools',
    `Bash(python3 -I ${skillDir}/scripts/mcp_stdio.py:*)`,
    ...['Write', 'Edit', 'MultiEdit'].map(tool => `${tool}(/${workDir}/.claude/**)`)];
}

/** Report servers that contribute at least one allowed tool. */
export const TOOL_SERVERS: readonly ReportServer[] = REPORT_SERVERS.filter(server => READ_TOOLS[server].length > 0);

/**
 * Permission rules for `--allowedTools`. All paths are absolute. File reads
 * are limited to `readDirs` (the skill, the run directory and the run's own
 * Claude session directory); writes to `outDir`, a subdirectory of the
 * working directory and never the working directory itself (its `.claude/`
 * would be a settings source).
 */
export function allowedTools(options: { skillDir: string; outDir: string; readDirs: readonly string[]; servers: readonly string[] }): string[] {
  // No Agent: a user agent definition could carry its own tools or permission mode.
  const rules: string[] = [];
  for (const directory of options.readDirs) {
    for (const tool of ['Read', 'Glob', 'Grep']) rules.push(`${tool}(/${directory}/**)`);
  }
  rules.push(`Write(/${options.outDir}/**)`);
  for (const script of SKILL_SCRIPTS) rules.push(`Bash(python3 -I ${options.skillDir}/scripts/${script}:*)`);
  for (const server of REPORT_SERVERS) {
    if (!options.servers.includes(server)) continue;
    for (const tool of READ_TOOLS[server]) {
      if (WRITE_TOOL_PATTERN.test(tool)) throw new Error('write tool in the report allowlist');
      rules.push(`mcp__${server}__${tool}`);
    }
  }
  return rules;
}
