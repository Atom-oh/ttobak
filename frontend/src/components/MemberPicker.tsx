'use client';

import { useState, useEffect, useRef } from 'react';
import { usersApi } from '@/lib/api';
import type { User } from '@/types/meeting';

interface MemberPickerProps {
  excludeUserIds: string[];
  onPick: (user: User) => void;
  placeholder?: string;
}

// Debounced user-search picker, cloned from ShareButton's search core
// (same 300ms/2-char pattern) but without the permission toggle/popover
// chrome that ShareButton bakes in -- role selection stays with the caller.
export function MemberPicker({ excludeUserIds, onPick, placeholder = 'Search by name or email' }: MemberPickerProps) {
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<User[]>([]);
  const [isSearching, setIsSearching] = useState(false);
  const timeoutRef = useRef<NodeJS.Timeout | null>(null);

  useEffect(() => {
    if (timeoutRef.current) clearTimeout(timeoutRef.current);
    if (query.length < 2) {
      setResults([]);
      return;
    }
    setIsSearching(true);
    timeoutRef.current = setTimeout(async () => {
      try {
        const { users } = await usersApi.search(query);
        setResults(users.filter((u) => !excludeUserIds.includes(u.userId)));
      } catch {
        setResults([]);
      } finally {
        setIsSearching(false);
      }
    }, 300);
    return () => {
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
    };
  }, [query, excludeUserIds]);

  return (
    <div>
      <input
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder={placeholder}
        className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
      />
      {query.length >= 2 && (
        <div className="mt-1 max-h-48 overflow-y-auto rounded-lg border border-slate-200 dark:border-white/10">
          {isSearching ? (
            <div className="p-3 text-center">
              <div className="animate-spin rounded-full h-4 w-4 border-2 border-primary border-t-transparent mx-auto" />
            </div>
          ) : results.length > 0 ? (
            results.map((user) => (
              <button
                key={user.userId}
                type="button"
                onClick={() => {
                  onPick(user);
                  setQuery('');
                  setResults([]);
                }}
                className="w-full flex items-center gap-2 px-3 py-2 text-left hover:bg-slate-50 dark:hover:bg-white/5"
              >
                <div className="w-6 h-6 rounded-full bg-primary/10 flex items-center justify-center text-primary text-xs font-bold shrink-0">
                  {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm text-slate-900 dark:text-text-main truncate">{user.name || user.email}</p>
                  {user.name && <p className="text-xs text-slate-500 dark:text-text-muted truncate">{user.email}</p>}
                </div>
                <span className="material-symbols-outlined text-primary text-lg">add</span>
              </button>
            ))
          ) : (
            <div className="p-3 text-center text-slate-400 dark:text-text-muted text-sm">No users found</div>
          )}
        </div>
      )}
    </div>
  );
}
