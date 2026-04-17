'use client';

import { useState } from 'react';

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    await navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <button
      onClick={handleCopy}
      className="shrink-0 p-1.5 rounded-md text-slate-400 hover:text-slate-600 hover:bg-slate-100 dark:text-[#849396] dark:hover:text-[#00E5FF] dark:hover:bg-white/5 transition-colors"
      title="Copy"
    >
      <span className="material-symbols-outlined text-base">
        {copied ? 'check' : 'content_copy'}
      </span>
    </button>
  );
}

function CodeBlock({ children }: { children: string }) {
  return (
    <div className="flex items-center gap-2 px-3 py-2.5 bg-slate-50 dark:bg-[#0e0e13] dark:border dark:border-white/10 rounded-lg font-mono text-xs text-slate-700 dark:text-[#bac9cc] overflow-x-auto">
      <code className="flex-1 whitespace-pre">{children}</code>
      <CopyButton text={children} />
    </div>
  );
}

function StepNumber({ n }: { n: number }) {
  return (
    <div className="shrink-0 w-6 h-6 rounded-full bg-primary/10 dark:bg-[#00E5FF]/10 flex items-center justify-center text-xs font-bold text-primary dark:text-[#00E5FF]">
      {n}
    </div>
  );
}

export function McpGuide() {
  const [expanded, setExpanded] = useState(false);

  return (
    <div className="glass-panel rounded-xl p-6">
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 bg-slate-100 dark:bg-white/5 rounded-lg flex items-center justify-center">
            <span className="material-symbols-outlined text-xl text-slate-600 dark:text-[#00E5FF]">
              terminal
            </span>
          </div>
          <div>
            <h3 className="text-base font-semibold text-slate-900 dark:text-[#e4e1e9]">
              Claude Code MCP
            </h3>
            <p className="text-sm text-slate-500 dark:text-[#849396]">
              Access meetings from Claude Code via MCP
            </p>
          </div>
        </div>
        <span className="text-xs font-semibold px-2 py-1 rounded-full bg-primary/10 text-primary dark:bg-[#B026FF]/10 dark:text-[#B026FF]">
          Guide
        </span>
      </div>

      {/* Summary */}
      <p className="text-sm text-slate-600 dark:text-[#bac9cc] mb-4">
        Connect your local Claude Code to Ttobak so it can list meetings, read summaries, and answer questions about your meeting history.
      </p>

      {/* Toggle */}
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-1.5 text-sm font-semibold text-primary dark:text-[#00E5FF] hover:underline mb-2"
      >
        <span className="material-symbols-outlined text-base transition-transform" style={{ transform: expanded ? 'rotate(90deg)' : undefined }}>
          chevron_right
        </span>
        {expanded ? 'Hide setup guide' : 'Show setup guide'}
      </button>

      {expanded && (
        <div className="mt-4 space-y-6">
          {/* Prerequisites */}
          <div className="flex items-start gap-2 p-3 bg-amber-50 dark:bg-amber-900/10 dark:border dark:border-amber-500/20 rounded-lg">
            <span className="material-symbols-outlined text-amber-600 dark:text-amber-400 text-lg shrink-0 mt-0.5">info</span>
            <p className="text-xs text-amber-800 dark:text-amber-300">
              Requires <strong>Node.js 18+</strong> and <strong>Claude Code CLI</strong> on your local machine. No git clone needed.
            </p>
          </div>

          {/* Step 1: Download */}
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <StepNumber n={1} />
              <h4 className="text-sm font-semibold text-slate-900 dark:text-[#e4e1e9]">Download</h4>
            </div>
            <p className="text-xs text-slate-500 dark:text-[#849396] ml-8">
              Download the MCP server (single file, ~700KB). No npm install needed.
            </p>
            <div className="ml-8">
              <CodeBlock>mkdir -p ~/.ttobak && curl -o ~/.ttobak/server.mjs https://ttobak.atomai.click/mcp/ttobak-mcp.mjs</CodeBlock>
            </div>
          </div>

          {/* Step 2: Register */}
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <StepNumber n={2} />
              <h4 className="text-sm font-semibold text-slate-900 dark:text-[#e4e1e9]">Register with Claude Code</h4>
            </div>
            <p className="text-xs text-slate-500 dark:text-[#849396] ml-8">
              Run this command in your terminal to add the Ttobak MCP server.
            </p>
            <div className="ml-8">
              <CodeBlock>{`claude mcp add --transport stdio --scope user -e TTOBAK_COGNITO_DOMAIN="https://ttobak-auth-180294183052.auth.ap-northeast-2.amazoncognito.com" -e TTOBAK_CLIENT_ID="33rh85mv6l9n7tn3s5h16prfdr" -e TTOBAK_API_URL="https://ttobak.atomai.click" ttobak -- node ~/.ttobak/server.mjs`}</CodeBlock>
            </div>
          </div>

          {/* Step 3: Authenticate */}
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <StepNumber n={3} />
              <h4 className="text-sm font-semibold text-slate-900 dark:text-[#e4e1e9]">Authenticate</h4>
            </div>
            <p className="text-xs text-slate-500 dark:text-[#849396] ml-8">
              Restart Claude Code, then ask it to log in. A browser window opens for Cognito login. Tokens auto-refresh for 30 days.
            </p>
            <div className="ml-8">
              <CodeBlock>Ttobak에 로그인해줘</CodeBlock>
            </div>
          </div>

          {/* Step 4: Verify */}
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <StepNumber n={4} />
              <h4 className="text-sm font-semibold text-slate-900 dark:text-[#e4e1e9]">Verify</h4>
            </div>
            <p className="text-xs text-slate-500 dark:text-[#849396] ml-8">
              Check MCP server status in Claude Code.
            </p>
            <div className="ml-8">
              <CodeBlock>/mcp</CodeBlock>
            </div>
          </div>

          {/* Available Tools */}
          <div className="space-y-3">
            <h4 className="text-sm font-semibold text-slate-900 dark:text-[#e4e1e9] flex items-center gap-2">
              <span className="material-symbols-outlined text-base text-primary dark:text-[#00E5FF]">build</span>
              Available Tools
            </h4>
            <div className="grid gap-2">
              {[
                { tool: 'ttobak_list_meetings', desc: 'List meetings with dates and status', prompt: 'Show my recent meetings' },
                { tool: 'ttobak_get_meeting', desc: 'Full detail: summary, transcript, action items', prompt: 'Get details of meeting X' },
                { tool: 'ttobak_ask', desc: 'RAG Q&A across all meetings', prompt: 'What did we decide about the API?' },
              ].map(({ tool, desc, prompt }) => (
                <div
                  key={tool}
                  className="flex items-start gap-3 p-3 bg-slate-50 dark:bg-[#0e0e13] dark:border dark:border-white/10 rounded-lg"
                >
                  <code className="shrink-0 text-xs font-semibold text-primary dark:text-[#00E5FF] bg-primary/5 dark:bg-[#00E5FF]/5 px-1.5 py-0.5 rounded">
                    {tool}
                  </code>
                  <div className="min-w-0">
                    <p className="text-xs text-slate-600 dark:text-[#bac9cc]">{desc}</p>
                    <p className="text-xs text-slate-400 dark:text-[#849396] mt-0.5 italic truncate">
                      &quot;{prompt}&quot;
                    </p>
                  </div>
                </div>
              ))}
            </div>
          </div>

          {/* Example Prompts */}
          <div className="space-y-3">
            <h4 className="text-sm font-semibold text-slate-900 dark:text-[#e4e1e9] flex items-center gap-2">
              <span className="material-symbols-outlined text-base text-primary dark:text-[#00E5FF]">chat</span>
              Example Prompts
            </h4>
            <div className="space-y-2">
              {[
                'List my Ttobak meetings from this week and summarize key decisions.',
                'Get meeting abc123 and list all action items with owners.',
                'Ask Ttobak: What topics came up in multiple meetings this month?',
              ].map((prompt) => (
                <div key={prompt} className="flex items-center gap-2">
                  <div className="flex-1 px-3 py-2 bg-slate-50 dark:bg-[#0e0e13] dark:border dark:border-white/10 rounded-lg text-xs text-slate-600 dark:text-[#bac9cc]">
                    {prompt}
                  </div>
                  <CopyButton text={prompt} />
                </div>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
