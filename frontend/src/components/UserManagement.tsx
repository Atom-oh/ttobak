'use client';

import { useEffect, useState, useCallback } from 'react';
import { adminUsersApi, type AdminUserSummary } from '@/lib/api';

// Dormancy is server-computed (see backend AdminUserSummary.dormant) — a
// missing lastLoginAt never counts as dormant, since on day one after this
// feature ships every existing user has no record yet. The frontend just
// renders whatever the backend decided.
function StatusBadge({ user }: { user: AdminUserSummary }) {
  if (!user.enabled) {
    return (
      <span className="text-xs font-semibold px-2 py-1 rounded-full bg-red-100 text-red-700 dark:bg-red-500/10 dark:text-red-400">
        비활성
      </span>
    );
  }
  if (user.status === 'FORCE_CHANGE_PASSWORD') {
    return (
      <span className="text-xs font-semibold px-2 py-1 rounded-full bg-blue-100 text-blue-700 dark:bg-blue-500/10 dark:text-blue-400">
        초대 대기
      </span>
    );
  }
  if (user.dormant) {
    return (
      <span className="text-xs font-semibold px-2 py-1 rounded-full bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-text-muted">
        휴면
      </span>
    );
  }
  return (
    <span className="text-xs font-semibold px-2 py-1 rounded-full bg-green-100 text-green-700 dark:bg-primary/10 dark:text-primary">
      활성
    </span>
  );
}

function formatDate(value: string | null): string {
  if (!value) return '기록 없음';
  const date = new Date(value);
  if (isNaN(date.getTime())) return '기록 없음';
  return date.toLocaleDateString('ko-KR', { year: 'numeric', month: 'short', day: 'numeric' });
}

export function UserManagement() {
  const [users, setUsers] = useState<AdminUserSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [warning, setWarning] = useState<string | null>(null);
  const [lastLoginUnavailable, setLastLoginUnavailable] = useState(false);
  // Tracks which user row currently has an action in flight, to disable its
  // buttons without blocking the rest of the table.
  const [actingOn, setActingOn] = useState<string | null>(null);

  const fetchUsers = useCallback(async () => {
    try {
      const data = await adminUsersApi.list();
      setUsers(data.users || []);
      setLastLoginUnavailable(Boolean(data.lastLoginUnavailable));
    } catch (err) {
      setError(err instanceof Error ? err.message : '사용자 목록을 불러오지 못했습니다');
    }
  }, []);

  useEffect(() => {
    (async () => {
      await fetchUsers();
      setLoading(false);
    })();
  }, [fetchUsers]);

  const runAction = async (
    userId: string,
    action: () => Promise<{ userId: string; warning?: string }>,
    successMessage: string
  ) => {
    setActingOn(userId);
    setError(null);
    setSuccess(null);
    setWarning(null);
    try {
      const res = await action();
      setSuccess(successMessage);
      if (res.warning) setWarning(res.warning);
      await fetchUsers();
    } catch (err) {
      setError(err instanceof Error ? err.message : '작업에 실패했습니다');
    } finally {
      setActingOn(null);
    }
  };

  const handleDelete = (user: AdminUserSummary) => {
    if (!confirm(`${user.email} 계정을 삭제하시겠습니까? Cognito 계정만 삭제되며 되돌릴 수 없습니다.`)) return;
    runAction(user.userId, () => adminUsersApi.delete(user.userId), `${user.email} 계정을 삭제했습니다.`);
  };

  const handleDisable = (user: AdminUserSummary) => {
    if (!confirm(`${user.email} 계정을 비활성화하시겠습니까?`)) return;
    runAction(user.userId, () => adminUsersApi.disable(user.userId), `${user.email} 계정을 비활성화했습니다.`);
  };

  const handleEnable = (user: AdminUserSummary) => {
    runAction(user.userId, () => adminUsersApi.enable(user.userId), `${user.email} 계정을 활성화했습니다.`);
  };

  const handleResendInvite = (user: AdminUserSummary) => {
    runAction(
      user.userId,
      () => adminUsersApi.resendInvite(user.userId),
      `${user.email}에게 초대 이메일을 다시 보냈습니다.`
    );
  };

  const handleResetPassword = (user: AdminUserSummary) => {
    if (!confirm(`${user.email} 계정의 비밀번호를 강제로 재설정하시겠습니까? 사용자에게 인증 코드 이메일이 발송됩니다.`)) return;
    runAction(
      user.userId,
      () => adminUsersApi.resetPassword(user.userId),
      `${user.email}에게 비밀번호 재설정 코드를 보냈습니다. 사용자는 로그인 화면의 "비밀번호를 잊으셨나요?"로 새 비밀번호를 설정할 수 있습니다.`
    );
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  return (
    <div className="dark:glass-panel dark:p-5 space-y-4">
      <p className="text-sm text-slate-600 dark:text-text-muted">
        가입한 사용자 목록과 최종 로그인 시각을 확인하고, 계정 활성화/비활성화, 삭제, 초대 재발송, 비밀번호 재설정을 관리할 수 있습니다.
      </p>

      {error && (
        <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-3">
          <p className="text-red-600 dark:text-red-400 text-sm">{error}</p>
        </div>
      )}
      {success && (
        <div className="bg-emerald-50 dark:bg-emerald-900/20 border border-emerald-200 dark:border-emerald-800 rounded-lg p-3">
          <p className="text-emerald-700 dark:text-emerald-400 text-sm">{success}</p>
        </div>
      )}
      {warning && (
        <div className="bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-lg p-3">
          <p className="text-amber-700 dark:text-amber-400 text-sm">{warning}</p>
        </div>
      )}
      {lastLoginUnavailable && (
        <div className="bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-lg p-3">
          <p className="text-amber-700 dark:text-amber-400 text-sm">
            최종 로그인 정보를 불러오지 못했습니다. 계정 관리는 정상적으로 동작합니다.
          </p>
        </div>
      )}

      {users.length > 0 ? (
        <div className="glass-panel rounded-xl overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-slate-50 dark:bg-surface-lowest border-b border-slate-200 dark:border-white/10">
                  <th className="text-left px-4 py-3 font-semibold text-slate-700 dark:text-text-secondary">이메일</th>
                  <th className="text-left px-4 py-3 font-semibold text-slate-700 dark:text-text-secondary">이름</th>
                  <th className="text-left px-4 py-3 font-semibold text-slate-700 dark:text-text-secondary">상태</th>
                  <th className="text-left px-4 py-3 font-semibold text-slate-700 dark:text-text-secondary">최종 로그인</th>
                  <th className="text-right px-4 py-3 font-semibold text-slate-700 dark:text-text-secondary">작업</th>
                </tr>
              </thead>
              <tbody>
                {users.map((user) => {
                  const busy = actingOn === user.userId;
                  return (
                    <tr key={user.userId} className="border-b border-slate-100 dark:border-white/5 last:border-b-0">
                      <td className="px-4 py-3 text-slate-900 dark:text-text-main font-medium">
                        {user.email}
                        {user.isAdmin && (
                          <span className="ml-2 text-xs font-semibold px-2 py-0.5 rounded-full bg-primary/10 text-primary">
                            관리자
                          </span>
                        )}
                      </td>
                      <td className="px-4 py-3 text-slate-600 dark:text-text-secondary">{user.name || '-'}</td>
                      <td className="px-4 py-3">
                        <StatusBadge user={user} />
                      </td>
                      <td className="px-4 py-3 text-slate-600 dark:text-text-secondary">
                        {formatDate(user.lastLoginAt)}
                      </td>
                      <td className="px-4 py-3 text-right">
                        <div className="flex items-center justify-end gap-1">
                          {user.status === 'FORCE_CHANGE_PASSWORD' ? (
                            <button
                              onClick={() => handleResendInvite(user)}
                              disabled={busy}
                              className="p-1.5 text-slate-400 hover:text-primary dark:text-text-muted dark:hover:text-primary rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 transition-colors disabled:opacity-40"
                              title="초대 메일 재발송"
                            >
                              <span className="material-symbols-outlined text-lg">forward_to_inbox</span>
                            </button>
                          ) : (
                            <button
                              onClick={() => handleResetPassword(user)}
                              disabled={busy}
                              className="p-1.5 text-slate-400 hover:text-primary dark:text-text-muted dark:hover:text-primary rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 transition-colors disabled:opacity-40"
                              title="비밀번호 재설정"
                            >
                              <span className="material-symbols-outlined text-lg">lock_reset</span>
                            </button>
                          )}
                          {user.enabled ? (
                            <button
                              onClick={() => handleDisable(user)}
                              disabled={busy}
                              className="p-1.5 text-slate-400 hover:text-amber-500 dark:text-text-muted dark:hover:text-amber-400 rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 transition-colors disabled:opacity-40"
                              title="비활성화"
                            >
                              <span className="material-symbols-outlined text-lg">block</span>
                            </button>
                          ) : (
                            <button
                              onClick={() => handleEnable(user)}
                              disabled={busy}
                              className="p-1.5 text-slate-400 hover:text-emerald-500 dark:text-text-muted dark:hover:text-emerald-400 rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 transition-colors disabled:opacity-40"
                              title="활성화"
                            >
                              <span className="material-symbols-outlined text-lg">check_circle</span>
                            </button>
                          )}
                          <button
                            onClick={() => handleDelete(user)}
                            disabled={busy}
                            className="p-1.5 text-slate-400 hover:text-red-500 dark:text-text-muted dark:hover:text-red-400 rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 transition-colors disabled:opacity-40"
                            title="삭제"
                          >
                            <span className="material-symbols-outlined text-lg">delete</span>
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      ) : (
        <div className="glass-panel rounded-xl p-8 text-center">
          <span className="material-symbols-outlined text-4xl text-slate-300 dark:text-text-muted mb-3 block">
            group
          </span>
          <p className="text-slate-500 dark:text-text-muted text-sm">가입된 사용자가 없습니다.</p>
        </div>
      )}
    </div>
  );
}
