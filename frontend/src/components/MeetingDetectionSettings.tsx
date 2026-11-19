'use client';

import { useEffect, useState } from 'react';
import { AstryxScope } from './astryx/AstryxScope';
import { Switch } from '@astryxdesign/core/Switch';
import { Text } from '@astryxdesign/core/Text';
import { VStack } from '@astryxdesign/core/VStack';
import { getMeetingDetection, isTauri, setMeetingDetectionEnabled } from '@/lib/tauri';

/** Mac app only: turns meeting detection prompts on or off (ADR-017).
 * Hidden in a browser or an app build without detection. */
export function MeetingDetectionSettings() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!isTauri()) return;
    let cancelled = false;
    void getMeetingDetection().then((state) => {
      if (!cancelled && state?.supported) setEnabled(state.enabled);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  if (enabled === null) return null;

  const toggle = async () => {
    const next = !enabled;
    setError(null);
    try {
      await setMeetingDetectionEnabled(next);
      setEnabled(next);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <section className="lg:pb-8 lg:border-b lg:border-slate-200 dark:lg:border-white/10">
      <h3 className="section-header mb-4">회의 감지 (Mac 앱)</h3>
      <AstryxScope>
        <VStack gap={2}>
          <Switch
            label="회의 감지"
            description="Zoom, Teams, Webex, Slack 허들, Google Meet 등이 마이크를 쓰기 시작하면 녹음을 제안합니다. 직접 시작을 눌러야 녹음됩니다."
            value={enabled}
            onChange={() => void toggle()}
            labelPosition="start"
            labelSpacing="spread"
            width="100%"
            status={error ? { type: 'error', message: error } : undefined}
          />
          <Text type="supporting" size="sm" as="p">
            브라우저 회의는 해당 브라우저에 대한 자동화 권한을 요청하며, 회의 주소만 확인합니다.
          </Text>
        </VStack>
      </AstryxScope>
    </section>
  );
}
