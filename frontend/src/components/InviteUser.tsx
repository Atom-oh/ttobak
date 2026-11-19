'use client';

import { useState } from 'react';
import { settingsApi } from '@/lib/api';
import { AstryxScope } from './astryx/AstryxScope';
import { Banner } from '@astryxdesign/core/Banner';
import { Button } from '@astryxdesign/core/Button';
import { CheckboxInput } from '@astryxdesign/core/CheckboxInput';
import { FormLayout } from '@astryxdesign/core/FormLayout';
import { Text } from '@astryxdesign/core/Text';
import { TextInput } from '@astryxdesign/core/TextInput';
import { VStack } from '@astryxdesign/core/VStack';

export function InviteUser() {
  const [email, setEmail] = useState('');
  const [name, setName] = useState('');
  const [makeAdmin, setMakeAdmin] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [warning, setWarning] = useState<string | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    // Astryx buttons use aria-disabled, so Enter in a field still submits while pending.
    if (submitting) return;
    const trimmed = email.trim().toLowerCase();
    if (!trimmed) return;

    setSubmitting(true);
    setError(null);
    setSuccess(null);
    setWarning(null);

    try {
      const res = await settingsApi.inviteUser({
        email: trimmed,
        name: name.trim() || undefined,
        admin: makeAdmin,
      });
      setSuccess(
        `${res.email} 님의 초대 이메일 발송을 요청했습니다. 로그인 주소와 임시 비밀번호가 포함됩니다. 수신되지 않으면 스팸함을 확인하고 사용자 관리에서 재발송하세요.${
          res.addedToAdmins ? ' (관리자 권한 부여됨)' : ''
        }`
      );
      if (makeAdmin && !res.addedToAdmins) {
        setWarning(`${res.email} 계정은 생성됐지만 관리자 권한 부여에는 실패했습니다. 다시 시도하거나 직접 admins 그룹에 추가해주세요.`);
      }
      setEmail('');
      setName('');
      setMakeAdmin(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : '사용자 초대에 실패했습니다');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <AstryxScope>
      <VStack gap={4}>
        <Text type="supporting" as="p">
          이메일 주소를 입력하면 해당 사용자에게 로그인 주소와 임시 비밀번호가 담긴 초대 메일 발송을 요청합니다.
          초대받은 사용자는 최초 로그인 시 새 비밀번호를 설정하게 됩니다.
        </Text>

        {error && <Banner status="error" title={error} />}
        {success && <Banner status="success" title={success} />}
        {warning && <Banner status="warning" title={warning} />}

        <form onSubmit={handleSubmit}>
          <FormLayout>
            <TextInput
              type="email"
              label="이메일"
              isRequired
              value={email}
              onChange={setEmail}
              placeholder="user@example.com"
              autoComplete="off"
              hasSpellCheck={false}
            />
            <TextInput
              label="이름"
              isOptional
              value={name}
              onChange={setName}
              placeholder="홍길동"
              autoComplete="off"
            />
            <CheckboxInput label="관리자 권한 부여" value={makeAdmin} onChange={setMakeAdmin} />
            <Button
              type="submit"
              variant="primary"
              label={submitting ? '초대 중...' : '사용자 초대'}
              isLoading={submitting}
              isDisabled={!email.trim()}
            />
          </FormLayout>
        </form>
      </VStack>
    </AstryxScope>
  );
}
