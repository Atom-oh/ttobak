'use client';

import type { ActionItem } from '@/types/meeting';

interface ActionItemsCardProps {
  items?: ActionItem[];
  onToggle: (itemId: string) => void;
}

export function ActionItemsCard({ items, onToggle }: ActionItemsCardProps) {
  return (
    <div className="bg-primary/5 dark:bg-surface-lowest border border-primary/20 dark:border-white/10 rounded-xl p-6">
      <div className="flex items-center gap-2 mb-4 text-primary">
        <span className="material-symbols-outlined">check_circle</span>
        <h3 className="font-bold dark:text-[#e4e1e9] dark:font-[var(--font-headline)]">Action Items</h3>
      </div>
      <div className="space-y-4">
        {items && items.length > 0 ? (
          items.map((item) => (
            <div key={item.id} className="flex items-start gap-3 dark:hover:bg-white/5 dark:rounded-lg dark:p-2 transition-colors">
              <input
                type="checkbox"
                checked={item.completed}
                onChange={() => onToggle(item.id)}
                className="mt-1 rounded border-primary/30 text-primary focus:ring-primary h-4 w-4 dark:accent-[#00E5FF]"
              />
              <div className="flex flex-col">
                <span className={`text-sm font-medium transition-all duration-200 ${item.completed ? 'line-through text-slate-400 dark:text-[#849396]' : 'text-slate-900 dark:text-[#e4e1e9]'}`}>
                  {item.text}
                </span>
                {(item.assignee || item.dueDate) && (
                  <span className="text-[10px] text-slate-400 dark:text-[#849396] mt-0.5">
                    {item.assignee && `Assigned to: @${item.assignee}`}
                    {item.assignee && item.dueDate && ' · '}
                    {item.dueDate && `Due ${item.dueDate}`}
                  </span>
                )}
              </div>
            </div>
          ))
        ) : (
          <p className="text-sm text-slate-400">액션 아이템이 없습니다.</p>
        )}
      </div>
    </div>
  );
}
