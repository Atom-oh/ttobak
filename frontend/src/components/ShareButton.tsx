'use client';

import { useState, useRef, useEffect } from 'react';
import { usersApi, meetingsApi } from '@/lib/api';
import type { User, SharedUser } from '@/types/meeting';

interface ShareButtonProps {
  meetingId: string;
  sharedWith?: SharedUser[];
  onShare?: (user: SharedUser) => void;
  onUnshare?: (userId: string) => void;
}

export function ShareButton({
  meetingId,
  sharedWith = [],
  onShare,
  onUnshare,
}: ShareButtonProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [searchResults, setSearchResults] = useState<User[]>([]);
  const [isSearching, setIsSearching] = useState(false);
  const [selectedPermission, setSelectedPermission] = useState<'read' | 'edit'>('read');
  const [isSharing, setIsSharing] = useState(false);
  const modalRef = useRef<HTMLDivElement>(null);
  const searchTimeoutRef = useRef<NodeJS.Timeout | null>(null);

  // Close on outside click
  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (modalRef.current && !modalRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    }

    if (isOpen) {
      document.addEventListener('mousedown', handleClickOutside);
    }
    return () => {
      document.removeEventListener('mousedown', handleClickOutside);
    };
  }, [isOpen]);

  // Search users with debounce
  useEffect(() => {
    if (searchTimeoutRef.current) {
      clearTimeout(searchTimeoutRef.current);
    }

    if (searchQuery.length < 2) {
      setSearchResults([]);
      return;
    }

    setIsSearching(true);
    searchTimeoutRef.current = setTimeout(async () => {
      try {
        const { users } = await usersApi.search(searchQuery);
        // Filter out already shared users
        const filtered = users.filter(
          (u) => !sharedWith.some((s) => s.userId === u.userId)
        );
        setSearchResults(filtered);
      } catch {
        setSearchResults([]);
      } finally {
        setIsSearching(false);
      }
    }, 300);

    return () => {
      if (searchTimeoutRef.current) {
        clearTimeout(searchTimeoutRef.current);
      }
    };
  }, [searchQuery, sharedWith]);

  const handleShare = async (user: User) => {
    setIsSharing(true);
    try {
      await meetingsApi.share(meetingId, {
        email: user.email,
        permission: selectedPermission,
      });
      onShare?.({
        userId: user.userId,
        email: user.email,
        name: user.name,
        permission: selectedPermission,
        sharedAt: new Date().toISOString(),
      });
      setSearchQuery('');
      setSearchResults([]);
    } catch (err) {
      console.error('Failed to share:', err);
    } finally {
      setIsSharing(false);
    }
  };

  const handleUnshare = async (userId: string) => {
    try {
      await meetingsApi.unshare(meetingId, userId);
      onUnshare?.(userId);
    } catch (err) {
      console.error('Failed to unshare:', err);
    }
  };

  return (
    <div className="relative">
      <button
        onClick={() => setIsOpen(true)}
        className="flex items-center gap-2 px-4 py-2 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-lg text-sm font-medium hover:bg-slate-50 dark:hover:bg-slate-700 transition-colors"
      >
        <span className="material-symbols-outlined text-lg">share</span>
        Share
        {sharedWith.length > 0 && (
          <span className="bg-primary/10 text-primary text-xs font-bold px-1.5 py-0.5 rounded-full">
            {sharedWith.length}
          </span>
        )}
      </button>

      {isOpen && (
        <div
          ref={modalRef}
          className="absolute right-0 top-full mt-2 w-80 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded-xl shadow-xl z-50"
        >
          <div className="p-4 border-b border-slate-200 dark:border-slate-700">
            <h3 className="font-bold text-slate-900 dark:text-white mb-3">Share meeting</h3>

            {/* Search Input */}
            <div className="relative">
              <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 text-lg">
                search
              </span>
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Search by name or email"
                className="w-full pl-10 pr-4 py-2 bg-slate-100 dark:bg-slate-800 border-none rounded-lg text-sm focus:ring-2 focus:ring-primary/20"
              />
            </div>

            {/* Permission Toggle */}
            <div className="flex gap-2 mt-3">
              <button
                onClick={() => setSelectedPermission('read')}
                className={`flex-1 py-1.5 text-xs font-medium rounded-lg transition-colors ${
                  selectedPermission === 'read'
                    ? 'bg-primary text-white'
                    : 'bg-slate-100 dark:bg-slate-800 text-slate-600 dark:text-slate-400'
                }`}
              >
                Can view
              </button>
              <button
                onClick={() => setSelectedPermission('edit')}
                className={`flex-1 py-1.5 text-xs font-medium rounded-lg transition-colors ${
                  selectedPermission === 'edit'
                    ? 'bg-primary text-white'
                    : 'bg-slate-100 dark:bg-slate-800 text-slate-600 dark:text-slate-400'
                }`}
              >
                Can edit
              </button>
            </div>
          </div>

          {/* Search Results */}
          {searchQuery.length >= 2 && (
            <div className="max-h-48 overflow-y-auto">
              {isSearching ? (
                <div className="p-4 text-center">
                  <div className="animate-spin rounded-full h-5 w-5 border-2 border-primary border-t-transparent mx-auto" />
                </div>
              ) : searchResults.length > 0 ? (
                searchResults.map((user) => (
                  <button
                    key={user.userId}
                    onClick={() => handleShare(user)}
                    disabled={isSharing}
                    className="w-full flex items-center gap-3 px-4 py-3 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50"
                  >
                    <div className="w-8 h-8 rounded-full bg-primary/10 flex items-center justify-center text-primary text-sm font-bold">
                      {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
                    </div>
                    <div className="flex-1 text-left">
                      <p className="text-sm font-medium text-slate-900 dark:text-white">
                        {user.name || user.email}
                      </p>
                      {user.name && (
                        <p className="text-xs text-slate-500">{user.email}</p>
                      )}
                    </div>
                    <span className="material-symbols-outlined text-primary">add</span>
                  </button>
                ))
              ) : (
                <div className="p-4 text-center text-slate-500 text-sm">
                  No users found
                </div>
              )}
            </div>
          )}

          {/* Currently Shared With */}
          {sharedWith.length > 0 && (
            <div className="border-t border-slate-200 dark:border-slate-700">
              <p className="px-4 py-2 text-xs font-bold text-slate-500 uppercase tracking-wider">
                Shared with
              </p>
              <div className="max-h-48 overflow-y-auto">
                {sharedWith.map((user) => (
                  <div
                    key={user.userId}
                    className="flex items-center gap-3 px-4 py-2"
                  >
                    <div className="w-8 h-8 rounded-full bg-slate-100 dark:bg-slate-800 flex items-center justify-center text-slate-500 text-sm font-bold">
                      {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-medium text-slate-900 dark:text-white truncate">
                        {user.name || user.email}
                      </p>
                      <p className="text-xs text-slate-500">
                        {user.permission === 'edit' ? 'Can edit' : 'Can view'}
                      </p>
                    </div>
                    <button
                      onClick={() => handleUnshare(user.userId)}
                      className="text-slate-400 hover:text-red-500 transition-colors"
                    >
                      <span className="material-symbols-outlined text-lg">close</span>
                    </button>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Close Button */}
          <div className="p-3 border-t border-slate-200 dark:border-slate-700">
            <button
              onClick={() => setIsOpen(false)}
              className="w-full py-2 text-sm font-medium text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-white transition-colors"
            >
              Done
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
