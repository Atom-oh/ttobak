'use client';

import { useState, useEffect, useRef, useCallback } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { researchChatApi } from '@/lib/api';
import type { ChatMessage } from '@/types/meeting';

interface ResearchChatProps {
  researchId: string;
  status: string;
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
  const [autoScroll, setAutoScroll] = useState(true);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const scrollContainerRef = useRef<HTMLDivElement>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const prevMessageCount = useRef(0);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const fetchMessages = useCallback(async () => {
    try {
      const data = await researchChatApi.listMessages(researchId);
      setMessages(data.messages || []);
    } catch {
      // silently ignore polling errors
    }
  }, [researchId]);

  useEffect(() => {
    fetchMessages();
    if (status === 'planning' || status === 'approved' || status === 'running') {
      pollRef.current = setInterval(fetchMessages, 3000);
    }
    return () => {
      if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null; }
    };
  }, [fetchMessages, status]);

  // Scroll only when new messages arrive AND user hasn't scrolled up
  useEffect(() => {
    if (messages.length > prevMessageCount.current && autoScroll) {
      messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
    }
    prevMessageCount.current = messages.length;
  }, [messages, autoScroll]);

  // Detect if user scrolled up
  const handleScroll = () => {
    const el = scrollContainerRef.current;
    if (!el) return;
    const isAtBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 100;
    setAutoScroll(isAtBottom);
  };

  const handleSend = async () => {
    if (!input.trim() || sending) return;
    const text = input.trim();
    setInput('');
    setSending(true);
    setAutoScroll(true);

    setMessages(prev => [...prev, {
      msgId: `temp-${Date.now()}`,
      role: 'user',
      content: text,
      createdAt: new Date().toISOString(),
    }]);

    try {
      await researchChatApi.sendMessage(researchId, { content: text });
      setTimeout(fetchMessages, 1000);
    } catch {
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

  const autoResize = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, 150) + 'px';
  }, []);

  useEffect(() => { autoResize(); }, [input, autoResize]);

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  const si = statusIndicator[status] || statusIndicator.planning;
  const inputDisabled = status === 'running' || status === 'approved';
  const isFullWidth = ['planning', 'running', 'approved'].includes(status);

  return (
    <div className={`flex flex-col bg-[#0e0e13] border-l border-white/10 h-full ${isFullWidth ? 'w-full' : 'w-[400px] flex-shrink-0'}`}>
      {/* Header */}
      <div className="px-5 py-3 border-b border-white/10">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold text-[#e4e1e9]">Research Assistant</h3>
          <div className="flex items-center gap-2">
            <span className={`w-2 h-2 rounded-full ${si.color}`} />
            <span className="text-xs text-[#849396]">{si.label}</span>
          </div>
        </div>
      </div>

      {/* Messages */}
      <div
        ref={scrollContainerRef}
        onScroll={handleScroll}
        className="flex-1 overflow-y-auto p-5 space-y-4"
      >
        {messages.length === 0 && (
          <div className="text-center py-12">
            <span className="material-symbols-outlined text-4xl text-[#849396]/30">forum</span>
            <p className="text-sm text-[#849396] mt-3">
              {status === 'planning' ? '에이전트가 연구 계획을 작성 중입니다...' : '메시지가 없습니다'}
            </p>
          </div>
        )}

        {messages.map((msg) => (
          <div key={msg.msgId} className={`flex ${msg.role === 'user' ? 'justify-end' : 'justify-start'}`}>
            <div
              className={`rounded-xl px-4 py-3 ${
                isFullWidth ? 'max-w-[700px]' : 'max-w-[90%]'
              } ${
                msg.role === 'user'
                  ? 'bg-[#00E5FF]/10 text-[#e4e1e9]'
                  : 'bg-white/[0.04] text-[#bac9cc]'
              }`}
            >
              {msg.role === 'agent' ? (
                <div className="prose prose-sm prose-invert max-w-none [&_h1]:text-lg [&_h1]:font-bold [&_h1]:mb-2 [&_h2]:text-base [&_h2]:font-bold [&_h2]:mt-4 [&_h2]:mb-2 [&_h3]:text-sm [&_h3]:font-semibold [&_h3]:mt-3 [&_h3]:mb-1 [&_p]:text-sm [&_p]:leading-relaxed [&_p]:mb-2 [&_li]:text-sm [&_li]:leading-relaxed [&_strong]:text-[#e4e1e9] [&_ul]:pl-4 [&_ol]:pl-4 [&_code]:bg-white/10 [&_code]:px-1 [&_code]:rounded [&_code]:text-xs [&_hr]:border-white/10 [&_hr]:my-3">
                  <ReactMarkdown remarkPlugins={[remarkGfm]}>{msg.content}</ReactMarkdown>
                </div>
              ) : (
                <div className="text-sm whitespace-pre-wrap break-words">{msg.content}</div>
              )}

              {msg.action === 'propose_structure' && status === 'planning' && (
                <button
                  onClick={handleApprove}
                  disabled={sending}
                  className="mt-3 w-full flex items-center justify-center gap-2 px-4 py-2 rounded-lg bg-[#00E5FF] text-[#0e0e13] text-sm font-semibold hover:bg-[#00E5FF]/80 disabled:opacity-50 transition-colors"
                >
                  <span className="material-symbols-outlined text-base">check_circle</span>
                  이 구조로 진행
                </button>
              )}

              <span className="block text-[10px] text-[#849396]/40 mt-2">
                {new Date(msg.createdAt).toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' })}
              </span>
            </div>
          </div>
        ))}

        {!autoScroll && messages.length > 0 && (
          <button
            onClick={() => { setAutoScroll(true); messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' }); }}
            className="sticky bottom-2 mx-auto flex items-center gap-1 px-3 py-1 rounded-full bg-[#00E5FF]/20 text-[#00E5FF] text-xs font-medium hover:bg-[#00E5FF]/30 transition-colors"
          >
            <span className="material-symbols-outlined text-sm">arrow_downward</span>
            최신 메시지
          </button>
        )}

        <div ref={messagesEndRef} />
      </div>

      {/* Running state banner */}
      {(status === 'running' || status === 'approved') && (
        <div className="mx-5 mb-2 flex items-center gap-3 px-4 py-3 rounded-xl bg-blue-500/10 border border-blue-500/20">
          <div className="animate-spin rounded-full h-5 w-5 border-2 border-[#00E5FF] border-t-transparent flex-shrink-0" />
          <div>
            <p className="text-sm font-medium text-[#e4e1e9]">리서치가 진행 중입니다</p>
            <p className="text-xs text-[#849396] mt-0.5">이 페이지를 닫아도 백그라운드에서 계속 진행됩니다. 완료되면 Insights에서 확인할 수 있습니다.</p>
          </div>
        </div>
      )}

      {/* Sub-page quick action */}
      {status === 'done' && (
        <div className="px-5 pb-2">
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
      <div className="p-4 border-t border-white/10">
        <div className="flex items-end gap-2">
          <textarea
            ref={textareaRef}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            disabled={inputDisabled}
            rows={1}
            placeholder={
              status === 'running' ? '리서치 진행 중...'
                : status === 'approved' ? '리서치 시작 대기 중...'
                : '질문이나 수정사항을 입력하세요... (Shift+Enter로 줄바꿈)'
            }
            className="flex-1 bg-white/[0.05] border border-white/10 rounded-lg px-3 py-2.5 text-sm text-[#e4e1e9] placeholder:text-[#849396]/60 focus:outline-none focus:border-[#00E5FF]/50 disabled:opacity-50 resize-none overflow-hidden"
          />
          <button
            onClick={handleSend}
            disabled={!input.trim() || sending || inputDisabled}
            title="전송 (Enter)"
            className="p-2.5 rounded-lg bg-[#00E5FF]/20 text-[#00E5FF] hover:bg-[#00E5FF]/30 disabled:opacity-30 transition-colors flex-shrink-0"
          >
            <span className="material-symbols-outlined text-lg">send</span>
          </button>
        </div>
      </div>
    </div>
  );
}
