'use client';

import { useCallback, useEffect, useState } from 'react';
import { apiFetch } from '@/lib/api';

interface ConnectorState {
  profile: 'read' | 'write';
  clientId: string;
  enabled: boolean;
  policyGeneration: number;
  maximum: string[];
  approval: { generation: number; capabilities: string[]; expiresAt: number; revoked: boolean; blocked: boolean } | null;
}

export function ConnectorSettings({ isAdmin }: { isAdmin: boolean }) {
  const [connectors, setConnectors] = useState<ConnectorState[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [stateCurrent, setStateCurrent] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [now, setNow] = useState(() => Date.now());
  const load = useCallback(async () => {
    const result = await apiFetch<{ connectors: ConnectorState[] }>('/api/connectors');
    setConnectors(result.connectors);
    setStateCurrent(true);
  }, []);

  useEffect(() => {
    let active = true;
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    apiFetch<{ connectors: ConnectorState[] }>('/api/connectors')
      .then(result => { if (active) { setConnectors(result.connectors); setStateCurrent(true); } })
      .catch(() => { if (active) setError('연결 권한을 불러오지 못했습니다. 새로고침 후 다시 시도해 주세요.'); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; clearInterval(timer); };
  }, []);

  async function change(item: ConnectorState, action: 'allow_read' | 'approve_write' | 'revoke' | 'block' | 'toggle_client') {
    if (busy || !stateCurrent) return;
    setBusy(true);
    setStateCurrent(false);
    setError('');
    setNotice('');
    try {
      if (action === 'toggle_client') {
        await apiFetch('/api/settings/connector-policy', { method: 'PUT', body: JSON.stringify({
          profile: item.profile, enabled: !item.enabled, expectedGeneration: item.policyGeneration,
        }) });
      } else {
        await apiFetch('/api/connectors/approval', { method: 'PUT', body: JSON.stringify({
          profile: item.profile, action, expectedGeneration: item.approval?.generation ?? 0,
        }) });
      }
      setNotice('연결 권한을 변경했습니다.');
    } catch {
      setError('변경 결과를 확인할 수 없습니다. 현재 상태를 다시 확인한 후 시도해 주세요.');
    } finally {
      try { await load(); }
      catch { setError('최신 상태를 불러오지 못했습니다. 새로고침 후 변경 결과를 확인해 주세요.'); }
      setBusy(false);
    }
  }

  if (loading) return <p className="text-sm text-slate-500" role="status">연결 권한 확인 중…</p>;
  if (!error && connectors.length === 0) return null;
  return (
    <section className="space-y-4" aria-label="AI 연결 권한">
      <h3 className="section-header">AI 연결 권한</h3>
      <p className="text-sm text-slate-600 dark:text-text-muted">
        연결된 AI는 내가 볼 수 있는 자료만 읽을 수 있습니다. 개인 메모 작성은 별도로 1시간 동안 허용할 수 있습니다.
      </p>
      {error && <p role="alert" className="text-sm text-red-600">{error}</p>}
      {notice && <p role="status" className="text-sm text-primary">{notice}</p>}
      {connectors.map(item => {
        const blocked = item.approval?.blocked === true;
        const writeApproval = !blocked && !item.approval?.revoked &&
          (item.approval?.expiresAt ?? 0) * 1000 > now && item.approval?.capabilities.includes('write');
        const writing = item.enabled && item.maximum.includes('write') && writeApproval;
        const controlsDisabled = busy || !stateCurrent;
        const label = item.profile === 'read' ? '자료 읽기 연결' : '개인 메모 작성 연결';
        const buttonClass = 'rounded-lg border border-slate-200 dark:border-white/10 px-3 py-2 text-sm disabled:opacity-40';
        return (
          <div key={item.clientId} className="rounded-xl border border-slate-200 dark:border-white/10 p-4 space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <h4 className="font-semibold">{label}</h4>
              <span className="text-sm text-slate-500">
                {!item.enabled ? '관리자에 의해 비활성화됨' : blocked ? '차단됨' : writing ? '읽기 및 개인 메모 작성 허용' : '읽기 허용'}
              </span>
            </div>
            <div className="flex flex-wrap gap-2">
              {blocked ? (
                <button className={buttonClass} disabled={controlsDisabled || !item.enabled} onClick={() => change(item, 'allow_read')}>읽기 허용</button>
              ) : (
                <button className={buttonClass} disabled={controlsDisabled || item.policyGeneration === 0} onClick={() => change(item, 'block')}>이 연결 차단</button>
              )}
              {item.profile === 'write' && (
                <button className={buttonClass} disabled={controlsDisabled || item.policyGeneration === 0 ||
                  (!writeApproval && (!item.enabled || blocked || !item.maximum.includes('write')))}
                  onClick={() => change(item, writeApproval ? 'revoke' : 'approve_write')}>
                  {writeApproval ? '쓰기 허용 해제' : '개인 메모 작성 1시간 허용'}
                </button>
              )}
              {isAdmin && (
                <button className={buttonClass} disabled={controlsDisabled} onClick={() => change(item, 'toggle_client')}>
                  {item.enabled ? '전체 사용자 연결 비활성화' : '전체 사용자 연결 활성화'}
                </button>
              )}
            </div>
          </div>
        );
      })}
    </section>
  );
}
