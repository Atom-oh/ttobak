'use client';

import { useCallback, useEffect, useState } from 'react';
import { apiFetch } from '@/lib/api';
import { AstryxScope } from './astryx/AstryxScope';
import { Badge } from '@astryxdesign/core/Badge';
import { Banner } from '@astryxdesign/core/Banner';
import { Button } from '@astryxdesign/core/Button';
import { Card } from '@astryxdesign/core/Card';
import { Heading } from '@astryxdesign/core/Heading';
import { HStack } from '@astryxdesign/core/HStack';
import { Text } from '@astryxdesign/core/Text';
import { VStack } from '@astryxdesign/core/VStack';

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

  if (loading) {
    return (
      <AstryxScope>
        <div role="status">
          <Text type="supporting">연결 권한 확인 중…</Text>
        </div>
      </AstryxScope>
    );
  }
  if (!error && connectors.length === 0) return null;
  return (
    <section aria-label="AI 연결 권한">
      <h3 className="section-header mb-4">AI 연결 권한</h3>
      <AstryxScope>
        <VStack gap={4}>
          <Text type="supporting" as="p">
            연결된 AI는 내가 볼 수 있는 자료만 읽을 수 있습니다. 개인 메모 작성은 별도로 1시간 동안 허용할 수 있습니다.
          </Text>
          {error && <Banner status="error" title={error} role="alert" />}
          {notice && <div role="status"><Banner status="success" title={notice} /></div>}
          {connectors.map(item => {
            const blocked = item.approval?.blocked === true;
            const writeApproval = !blocked && !item.approval?.revoked &&
              (item.approval?.expiresAt ?? 0) * 1000 > now && item.approval?.capabilities.includes('write');
            const writing = item.enabled && item.maximum.includes('write') && writeApproval;
            const controlsDisabled = busy || !stateCurrent;
            const label = item.profile === 'read' ? '자료 읽기 연결' : '개인 메모 작성 연결';
            const status = !item.enabled
              ? { label: '관리자에 의해 비활성화됨', variant: 'neutral' as const }
              : blocked ? { label: '차단됨', variant: 'error' as const }
                : writing ? { label: '읽기 및 개인 메모 작성 허용', variant: 'warning' as const }
                  : { label: '읽기 허용', variant: 'success' as const };
            return (
              <Card key={item.clientId}>
                <VStack gap={3}>
                  <HStack gap={2} vAlign="center" wrap="wrap">
                    <Heading level={4}>{label}</Heading>
                    <Badge label={status.label} variant={status.variant} />
                  </HStack>
                  <HStack gap={2} wrap="wrap">
                    {blocked ? (
                      <Button label="읽기 허용" isDisabled={controlsDisabled || !item.enabled}
                        onClick={() => change(item, 'allow_read')} />
                    ) : (
                      <Button label="이 연결 차단" variant="destructive" isDisabled={controlsDisabled || item.policyGeneration === 0}
                        onClick={() => change(item, 'block')} />
                    )}
                    {item.profile === 'write' && (
                      <Button
                        label={writeApproval ? '쓰기 허용 해제' : '개인 메모 작성 1시간 허용'}
                        isDisabled={controlsDisabled || item.policyGeneration === 0 ||
                          (!writeApproval && (!item.enabled || blocked || !item.maximum.includes('write')))}
                        onClick={() => change(item, writeApproval ? 'revoke' : 'approve_write')}
                      />
                    )}
                    {isAdmin && (
                      <Button
                        variant="ghost"
                        label={item.enabled ? '전체 사용자 연결 비활성화' : '전체 사용자 연결 활성화'}
                        isDisabled={controlsDisabled}
                        onClick={() => change(item, 'toggle_client')}
                      />
                    )}
                  </HStack>
                </VStack>
              </Card>
            );
          })}
        </VStack>
      </AstryxScope>
    </section>
  );
}
