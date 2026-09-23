'use client';

import { useEffect, useId, useRef, useState } from 'react';
import {
  MCP_CLIENTS, loadMcpGuideConfig, mcpClientGuide,
  type GuideCode, type McpClient, type McpGuideConfig, type McpTransport,
} from '@/lib/mcpGuide';

function CopyButton({ text, label, disabled = false }: { text: string; label: string; disabled?: boolean }) {
  const [status, setStatus] = useState<{ text: string; result: 'copied' | 'failed' } | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => { if (timer.current) clearTimeout(timer.current); }, []);
  const current = status?.text === text ? status.result : null;
  const copy = async () => {
    try {
      if (!navigator.clipboard) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(text);
      setStatus({ text, result: 'copied' });
    } catch {
      setStatus({ text, result: 'failed' });
    }
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setStatus(null), 2500);
  };
  return (
    <div className="flex shrink-0 flex-col items-end gap-1">
      <button type="button" onClick={copy} disabled={disabled}
        aria-label={`${label} 복사`} title={disabled ? '연결 설정을 확인한 뒤 복사할 수 있습니다' : `${label} 복사`}
        className="rounded-md p-2 text-slate-400 transition-colors hover:bg-slate-100 hover:text-slate-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary disabled:cursor-not-allowed disabled:opacity-35 dark:text-text-muted dark:hover:bg-white/5 dark:hover:text-primary">
        <span aria-hidden="true" className="material-symbols-outlined text-base">{current === 'copied' ? 'check' : 'content_copy'}</span>
      </button>
      <span role="status" className="max-w-32 text-right text-xs text-slate-500 dark:text-text-muted">
        {current === 'copied' ? '복사됨' : current === 'failed' ? '복사 실패 · 내용을 직접 선택해 주세요' : ''}
      </span>
    </div>
  );
}

function CodeBlock({ code, configured, httpConfigured }: { code: GuideCode; configured: boolean; httpConfigured: boolean }) {
  const disabled = Boolean((code.needsConfig && !configured) || (code.needsHttp && !httpConfigured));
  return (
    <div className="min-w-0 space-y-1.5">
      <p className="text-xs font-medium text-slate-500 dark:text-text-muted">{code.title}</p>
      <div className="flex min-w-0 items-start gap-2 rounded-lg border border-slate-200 bg-slate-50 px-3 py-2.5 dark:border-white/10 dark:bg-surface-lowest">
        <pre className="min-w-0 flex-1 overflow-x-auto py-1 font-mono text-xs leading-relaxed text-slate-700 dark:text-text-secondary"><code>{code.text}</code></pre>
        <CopyButton text={code.text} label={code.title} disabled={disabled} />
      </div>
    </div>
  );
}

type LoadState = { status: 'loading' } | { status: 'ready'; config: McpGuideConfig } | { status: 'error'; message: string };

export function McpGuide() {
  const id = useId();
  const [expanded, setExpanded] = useState(false);
  const [client, setClient] = useState<McpClient>('claude');
  const [transport, setTransport] = useState<McpTransport>('stdio');
  const [load, setLoad] = useState<LoadState>({ status: 'loading' });
  const [retry, setRetry] = useState(0);
  const selected = MCP_CLIENTS.find(candidate => candidate.id === client)!;
  const config = load.status === 'ready' ? load.config : null;
  const guide = mcpClientGuide(client, transport, config);

  useEffect(() => {
    if (!expanded) return;
    let cancelled = false;
    const controller = new AbortController();
    const timeout = setTimeout(() => {
      controller.abort();
      if (!cancelled) setLoad({ status: 'error', message: '연결 설정 요청이 지연되고 있습니다. 잠시 후 다시 시도해 주세요.' });
    }, 6000);
    loadMcpGuideConfig(window.location.origin, controller.signal)
      .then(next => { if (!cancelled) setLoad({ status: 'ready', config: next }); })
      .catch((error: unknown) => {
        if (cancelled) return;
        const message = error instanceof Error && error.name !== 'AbortError'
          ? error.message : '연결 설정 요청이 지연되고 있습니다. 잠시 후 다시 시도해 주세요.';
        setLoad({ status: 'error', message: message.slice(0, 200) });
      })
      .finally(() => clearTimeout(timeout));
    return () => { cancelled = true; controller.abort(); clearTimeout(timeout); };
  }, [expanded, retry]);

  const select = (next: McpClient) => {
    setExpanded(true);
    if (next === client) return;
    setClient(next);
    setTransport(next === 'quick' ? 'http' : 'stdio');
  };

  return (
    <div className="glass-panel min-w-0 rounded-xl p-4 sm:p-6">
      <div className="mb-4 flex items-start justify-between gap-3">
        <div className="flex items-center gap-3">
          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-slate-100 dark:bg-white/5">
            <span aria-hidden="true" className="material-symbols-outlined text-xl text-slate-600 dark:text-primary">terminal</span>
          </div>
          <div>
            <h3 className="text-base font-semibold text-slate-900 dark:text-text-main">MCP 연결 가이드</h3>
            <p className="mt-0.5 text-sm text-slate-500 dark:text-text-muted">사용하는 AI 도구에서 TTOBAK의 회의와 문서를 읽고 활용하세요.</p>
          </div>
        </div>
      </div>

      <div role="group" aria-label="MCP 클라이언트 선택" className="flex flex-wrap gap-2">
        {MCP_CLIENTS.map(candidate => (
          <button key={candidate.id} type="button" aria-pressed={client === candidate.id} onClick={() => select(candidate.id)}
            className={`min-h-10 rounded-lg border px-3 py-2 text-sm font-medium transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary ${client === candidate.id
              ? 'border-primary/40 bg-primary/10 text-primary'
              : 'border-slate-200 text-slate-600 hover:bg-slate-50 dark:border-white/10 dark:text-text-secondary dark:hover:bg-white/5'}`}>
            {candidate.name}
          </button>
        ))}
      </div>
      <button type="button" aria-expanded={expanded} aria-controls={`${id}-guide`} onClick={() => setExpanded(value => !value)}
        className="mt-4 flex items-center gap-1.5 rounded text-sm font-semibold text-primary hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary">
        <span aria-hidden="true" className="material-symbols-outlined text-base transition-transform" style={{ transform: expanded ? 'rotate(90deg)' : undefined }}>chevron_right</span>
        {expanded ? '연결 가이드 접기' : '연결 가이드 펼치기'}
      </button>

      {expanded && (
        <div id={`${id}-guide`} className="mt-5 min-w-0 space-y-5" aria-label={`${selected.name} 연결 가이드`}>
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 pb-4 dark:border-white/10">
            <div>
              <h4 className="text-sm font-semibold text-slate-900 dark:text-text-main">{selected.name}</h4>
              <p className="mt-0.5 text-xs text-slate-500 dark:text-text-muted">{selected.description}{!selected.local && ' · HTTP 전용'}</p>
            </div>
            <div role="group" aria-label="MCP 연결 방식" className="inline-flex rounded-lg bg-slate-100 p-1 dark:bg-white/5">
              {(['stdio', 'http'] as const).map(mode => (
                <button key={mode} type="button" disabled={mode === 'stdio' && !selected.local}
                  aria-pressed={transport === mode} onClick={() => setTransport(mode)}
                  className={`min-h-9 rounded-md px-3 py-1.5 text-xs font-semibold transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary disabled:cursor-not-allowed disabled:opacity-35 ${transport === mode
                    ? 'bg-white text-primary shadow-sm dark:bg-surface-lowest' : 'text-slate-500 dark:text-text-muted'}`}>
                  {mode === 'stdio' ? '로컬 · stdio' : '원격 · HTTP'}
                </button>
              ))}
            </div>
          </div>

          <p className="text-sm leading-relaxed text-slate-600 dark:text-text-secondary">{guide.introduction}</p>
          {load.status === 'loading' && <p role="status" className="text-xs text-slate-500 dark:text-text-muted">사이트의 연결 설정을 불러오는 중입니다…</p>}
          {load.status === 'error' && (
            <div role="alert" className="rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs leading-relaxed text-amber-800 dark:border-amber-500/20 dark:bg-amber-900/10 dark:text-amber-300">
              <p>{load.message}</p>
              <button type="button" onClick={() => { setLoad({ status: 'loading' }); setRetry(value => value + 1); }} className="mt-2 rounded font-semibold underline focus-visible:outline-2 focus-visible:outline-primary">다시 불러오기</button>
            </div>
          )}
          {transport === 'http' && (
            <div className="flex items-start gap-2 rounded-lg border border-slate-200 bg-slate-50 p-3 dark:border-white/10 dark:bg-surface-lowest">
              <span aria-hidden="true" className="material-symbols-outlined shrink-0 text-base text-primary">info</span>
              <div className="min-w-0 text-xs leading-relaxed text-slate-600 dark:text-text-secondary">
                <p className="font-semibold text-slate-800 dark:text-text-main">{load.status === 'loading' ? 'HTTP 주소 확인 중' : load.status === 'error' ? 'HTTP 주소 확인 불가' : config?.httpUrl ? 'HTTP 주소 등록됨 · OAuth 설정 확인 필요' : 'HTTP 주소 미설정'}</p>
                <p className="mt-1">{load.status !== 'ready'
                  ? '사이트의 연결 설정을 확인한 뒤 HTTP 주소가 필요한 명령을 복사할 수 있습니다.'
                  : config?.httpUrl
                    ? '아래는 사이트에 등록된 주소입니다. 연결 전에 해당 클라이언트의 OAuth 콜백 등록과 본인 로그인을 확인하세요.'
                    : '이 사이트에 공개 HTTP MCP 주소가 아직 등록되지 않았습니다. 아래는 설정 절차 안내이며, 주소가 필요한 명령의 복사는 비활성화됩니다. 관리자에게 서버 주소와 인증 설정을 확인해 주세요.'}</p>
              </div>
            </div>
          )}

          <ol className="min-w-0 space-y-5">
            {guide.steps.map((step, index) => (
              <li key={`${client}-${transport}-${step.title}`} className="flex min-w-0 items-start gap-3">
                <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-bold text-primary">{index + 1}</span>
                <div className="min-w-0 flex-1 space-y-2.5">
                  <h5 className="text-sm font-semibold text-slate-900 dark:text-text-main">{step.title}</h5>
                  <p className="text-xs leading-relaxed text-slate-500 dark:text-text-muted">{step.description}</p>
                  {step.code?.map(code => <CodeBlock key={code.title} code={code} configured={Boolean(config)} httpConfigured={Boolean(config?.httpUrl)} />)}
                </div>
              </li>
            ))}
          </ol>

          {guide.note && <p className="rounded-lg bg-primary/5 px-3 py-2.5 text-xs leading-relaxed text-slate-600 dark:text-text-secondary">{guide.note}</p>}
          {client === 'kiro-cli' && (
            <CodeBlock code={{ title: '선택 사항 · Tool Search 활성화', text: 'kiro-cli settings toolSearch.enabled true' }} configured httpConfigured={Boolean(config?.httpUrl)} />
          )}
          <a href={selected.docs} target="_blank" rel="noopener noreferrer" className="inline-flex rounded text-xs font-semibold text-primary hover:underline focus-visible:outline-2 focus-visible:outline-primary">{selected.name} 공식 MCP 문서 ↗</a>

          <div className="space-y-3 border-t border-slate-200 pt-4 dark:border-white/10">
            <h4 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-text-main"><span aria-hidden="true" className="material-symbols-outlined text-base text-primary">chat</span>연결 후 이렇게 요청하세요</h4>
            {[
              'TTOBAK에서 이번 주 내 회의 목록을 보여줘',
              'TTOBAK에서 이 회의의 저장된 노트를 읽어줘',
              'TTOBAK에서 이 회의의 AI 요약과 액션 아이템을 확인해줘',
            ].map(prompt => (
              <div key={prompt} className="flex min-w-0 items-start gap-2 rounded-lg bg-slate-50 px-3 py-2 dark:bg-surface-lowest">
                <p className="min-w-0 flex-1 py-1 text-xs leading-relaxed text-slate-600 dark:text-text-secondary">{prompt}</p>
                <CopyButton text={prompt} label="예시 요청" />
              </div>
            ))}
            <p className="text-xs leading-relaxed text-slate-500 dark:text-text-muted">회의 읽기는 저장된 노트를 우선 사용합니다. AI 요약이나 원문 녹취가 필요하면 구분해서 요청하세요. 접근 가능한 본인·공유 자료의 권한은 TTOBAK에서 확인합니다.</p>
          </div>
        </div>
      )}
    </div>
  );
}
