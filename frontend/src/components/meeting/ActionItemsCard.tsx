'use client';

import type { ActionItem } from '@/types/meeting';

interface ActionItemsCardProps {
  items?: ActionItem[];
  onToggle: (itemId: string) => void;
}

export function ActionItemsCard({ items, onToggle }: ActionItemsCardProps) {
  return (
    <div className="bg-[var(--color-primary)]/5 dark:bg-[var(--color-primary)]/10 border border-[var(--color-primary)]/20 rounded-xl p-6">
      <div className="flex items-center gap-2 mb-4 text-[var(--color-primary)]">
        <span className="material-symbols-outlined">check_circle</span>
        <h3 className="font-bold">Action Items</h3>
      </div>
      <div className="space-y-4">
        {items && items.length > 0 ? (
          items.map((item) => (
            <div key={item.id} className="flex items-start gap-3">
              <input
                type="checkbox"
                checked={item.completed}
                onChange={() => onToggle(item.id)}
                className="mt-1 rounded border-[var(--color-primary)]/30 text-[var(--color-primary)] focus:ring-[var(--color-primary)] h-4 w-4"
              />
              <div className="flex flex-col">
                <span className={`text-sm font-medium transition-all duration-200 ${item.completed ? 'line-through text-[var(--color-text-muted)]' : 'text-[var(--color-text-primary)]'}`}>
                  {item.text}
                </span>
                {(item.assignee || item.dueDate) && (
                  <span className="text-[10px] text-[var(--color-text-muted)] mt-0.5">
                    {item.assignee && `Assigned to: @${item.assignee}`}
                    {item.assignee && item.dueDate && ' · '}
                    {item.dueDate && `Due ${item.dueDate}`}
                  </span>
                )}
              </div>
            </div>
          ))
        ) : (
          <p className="text-sm text-[var(--color-text-muted)]">액션 아이템이 없습니다.</p>
        )}
      </div>
    </div>
  );
}
