'use client';

import { useState, useEffect, useRef, useCallback } from 'react';
import { researchChatApi } from '@/lib/api';
import type { ChatMessage } from '@/types/meeting';

interface ResearchChatProps {
  researchId: string;
  status: string; // planning | approved | running | done | error
  onApprove: () => void;
  onSubPageCreated: () => void;
}

const statusIndicator: Record<string, { color: string; label: string }> = {
  planning: { color: 'bg-amber-400', label: '구조 계획 중' },
  approved: { color: 'bg-blue-400', label: '승인됨' },
  running:  { color: 'bg-blue-400 animate-pulse', label: '리서치 진행 중' },
  done:     { color: 'bg-emerald-400', label: '완료' },
  error:    { color: 'bg-red-400', label: '오류' },
};

export function ResearchChat({ researchId, status, onApprove, onSubPageCreated }: ResearchChatProps) {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState('');
  const [sending, setSending] = useState(false);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchMessages = useCallback(async () => {
    try {
      const data = await researchChatApi.listMessages(researchId);
      setMessages(data.messages || []);
    } catch {
      // silently ignore polling errors
    }
  }, [researchId]);

  // Poll messages during planning/approved, stop when done
  useEffect(() => {
    fetchMessages();

    if (status === 'planning' || status === 'approved' || status === 'running') {
      pollRef.current = setInterval(fetchMessages, 3000);
    }

    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, [fetchMessages, status]);

  // Auto-scroll to bottom on new messages
  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  const handleSend = async () => {
    if (!input.trim() || sending) return;
    const text = input.trim();
    setInput('');
    setSending(true);

    // Optimistically add user message
    setMessages(prev => [...prev, {
      msgId: `temp-${Date.now()}`,
      role: 'user',
      content: text,
      createdAt: new Date().toISOString(),
    }]);

    try {
      await researchChatApi.sendMessage(researchId, { content: text });
      // Refresh to get agent response
      setTimeout(fetchMessages, 1000);
    } catch {
      // Remove optimistic message on error
      setMessages(prev => prev.filter(m => !m.msgId.startsWith('temp-')));
    } finally {
      setSending(false);
    }
  };

  const handleApprove = async () => {
    setSending(true);
    try {
      await researchChatApi.sendMessage(researchId, { content: 'Approved', action: 'approve' });
      onApprove();
    } finally {
      setSending(false);
    }
  };

  const handleRequestSubPage = async (topic: string) => {
    setSending(true);
    try {
      await researchChatApi.sendMessage(researchId, { content: topic, action: 'request_subpage' });
      onSubPageCreated();
    } finally {
      setSending(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  const si = statusIndicator[status] || statusIndicator.planning;
  const inputDisabled = status === 'running' || status === 'approved';

  return (
    <div className="w-[360px] flex-shrink-0 flex flex-col bg-[#0e0e13] border-l border-white/10 h-full">
      {/* Header */}
      <div className="px-4 py-3 border-b border-white/10">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold text-[#e4e1e9]">Research Assistant</h3>
          <div className="flex items-center gap-2">
            <span className={`w-2 h-2 rounded-full ${si.color}`} />
            <span className="text-xs text-[#849396]">{si.label}</span>
          </div>
        </div>
      </div>

      {/* Messages */}
      <div className="flex-1 overflow-y-auto p-4 space-y-3">
        {messages.length === 0 && (
          <div className="text-center py-8">
            <span className="material-symbols-outlined text-3xl text-[#849396]/50">forum</span>
            <p className="text-xs text-[#849396] mt-2">
              {status === 'planning' ? '에이전트가 구조를 제안할 예정입니다...' : '메시지가 없습니다'}
            </p>
          </div>
        )}

        {messages.map((msg) => (
          <div key={msg.msgId} className={`flex ${msg.role === 'user' ? 'justify-end' : 'justify-start'}`}>
            <div
              className={`max-w-[85%] rounded-xl px-3 py-2 text-sm ${
                msg.role === 'user'
                  ? 'bg-[#00E5FF]/15 text-[#e4e1e9]'
                  : 'bg-white/[0.05] text-[#bac9cc]'
              }`}
            >
              <div className="whitespace-pre-wrap break-words">{msg.content}</div>
              {/* Approve button for propose_structure messages */}
              {msg.action === 'propose_structure' && status === 'planning' && (
                <button
                  onClick={handleApprove}
                  disabled={sending}
                  className="mt-2 w-full flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-lg bg-[#00E5FF] text-[#0e0e13] text-xs font-semibold hover:bg-[#00E5FF]/80 disabled:opacity-50 transition-colors"
                >
                  <span className="material-symbols-outlined text-sm">check_circle</span>
                  이 구조로 진행
                </button>
              )}
              <span className="block text-[10px] text-[#849396]/60 mt-1">
                {new Date(msg.createdAt).toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' })}
              </span>
            </div>
          </div>
        ))}

        <div ref={messagesEndRef} />
      </div>

      {/* Sub-page quick action (when done) */}
      {status === 'done' && (
        <div className="px-4 pb-2">
          <button
            onClick={() => handleRequestSubPage('추가 하위 페이지')}
            disabled={sending}
            className="w-full flex items-center justify-center gap-1.5 px-3 py-2 rounded-lg border border-[#00E5FF]/30 text-[#00E5FF] text-xs font-semibold hover:bg-[#00E5FF]/10 disabled:opacity-50 transition-colors"
          >
            <span className="material-symbols-outlined text-sm">add_circle</span>
            하위 페이지 추가
          </button>
        </div>
      )}

      {/* Input */}
      <div className="p-3 border-t border-white/10">
        <div className="flex items-center gap-2">
          <input
            type="text"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            disabled={inputDisabled}
            placeholder={
              status === 'running' ? '리서치 진행 중...'
                : status === 'approved' ? '리서치 시작 대기 중...'
                : '메시지를 입력하세요...'
            }
            className="flex-1 bg-white/[0.05] border border-white/10 rounded-lg px-3 py-2 text-sm text-[#e4e1e9] placeholder:text-[#849396]/60 focus:outline-none focus:border-[#00E5FF]/50 disabled:opacity-50"
          />
          <button
            onClick={handleSend}
            disabled={!input.trim() || sending || inputDisabled}
            className="p-2 rounded-lg bg-[#00E5FF]/20 text-[#00E5FF] hover:bg-[#00E5FF]/30 disabled:opacity-30 transition-colors"
          >
            <span className="material-symbols-outlined text-lg">send</span>
          </button>
        </div>
      </div>
    </div>
  );
}
