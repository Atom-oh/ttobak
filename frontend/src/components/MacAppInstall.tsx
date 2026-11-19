'use client';

import { useEffect, useState } from 'react';
import { MAC_INSTALL_COMMAND, publishedVersion } from '@/lib/macRelease';
import { CopyButton } from '@/components/McpGuide';
import { AstryxScope } from './astryx/AstryxScope';
import { CodeBlock } from '@astryxdesign/core/CodeBlock';
import { Text } from '@astryxdesign/core/Text';
import { VStack } from '@astryxdesign/core/VStack';

export function MacAppInstall() {
  const [version, setVersion] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    (async () => {
      try {
        const res = await fetch('/mac/latest.json', { cache: 'no-store', signal: controller.signal });
        if (!res.ok) return;
        setVersion(publishedVersion(await res.json()));
      } catch {
        // Hide the version line on any failure.
      }
    })();
    return () => controller.abort();
  }, []);

  return (
    <AstryxScope>
      <VStack gap={3}>
        <Text type="supporting" as="p">
          Apple Silicon Mac(macOS 14.2 이상)에서 터미널에 아래 명령어를 붙여넣어 설치합니다.
        </Text>
        {/* The app's CopyButton instead of CodeBlock's: Astryx's copy button shows an
            anchored tooltip, which older macOS app WebViews cannot position. */}
        <CodeBlock
          code={MAC_INSTALL_COMMAND}
          language="bash"
          hasLanguageLabel={false}
          hasCopyButton={false}
          endContent={<CopyButton text={MAC_INSTALL_COMMAND} label="설치 명령어" />}
          width="100%"
          size="sm"
        />
        {version && <Text type="supporting" size="sm">현재 버전 v{version}</Text>}
      </VStack>
    </AstryxScope>
  );
}
