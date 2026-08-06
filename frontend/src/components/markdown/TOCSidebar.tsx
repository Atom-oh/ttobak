'use client';
import { useState, useEffect, useRef, RefObject } from 'react';

interface TocItem {
  id: string;
  text: string;
  level: number;
}

interface TOCSidebarProps {
  contentRef: RefObject<HTMLDivElement | null>;
}

export function TOCSidebar({ contentRef }: TOCSidebarProps) {
  const [items, setItems] = useState<TocItem[]>([]);
  const [activeId, setActiveId] = useState<string>('');
  const observerRef = useRef<IntersectionObserver | null>(null);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      const el = contentRef.current;
      if (!el) return;

      const headings = el.querySelectorAll('h2, h3');
      const tocItems: TocItem[] = [];
      headings.forEach((heading) => {
        const id = heading.id;
        const text = heading.textContent ?? '';
        if (!id || !text) return;
        const level = heading.tagName === 'H2' ? 0 : 1;
        tocItems.push({ id, text, level });
      });
      setItems(tocItems);

      if (tocItems.length < 2) return;

      observerRef.current = new IntersectionObserver(
        (entries) => {
          for (const entry of entries) {
            if (entry.isIntersecting) {
              setActiveId(entry.target.id);
            }
          }
        },
        { rootMargin: '-80px 0px -60% 0px', threshold: 0.1 }
      );

      headings.forEach((heading) => {
        if (heading.id) observerRef.current?.observe(heading);
      });
    });

    return () => {
      window.cancelAnimationFrame(frame);
      observerRef.current?.disconnect();
    };
  }, [contentRef]);

  if (items.length < 2) return null;

  const handleClick = (id: string) => {
    const el = document.getElementById(id);
    el?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  };

  return (
    <nav className="hidden xl:block w-[264px] shrink-0 sticky top-24 self-start max-h-[calc(100vh-8rem)] overflow-y-auto">
      <p className="text-[11px] font-semibold text-text-muted tracking-[2px] mb-4">
        ON THIS PAGE
      </p>
      <ul className="space-y-1">
        {items.map((item) => {
          const isActive = activeId === item.id;
          return (
            <li key={item.id} className="relative">
              {isActive && (
                <span className="absolute left-0 top-1 bottom-1 w-0.5 rounded-full bg-primary" />
              )}
              <button
                onClick={() => handleClick(item.id)}
                className={`block w-full rounded-sm text-left text-[13px] leading-snug py-1 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 ${
                  item.level === 0 ? 'pl-3' : 'pl-6'
                } ${
                  isActive
                    ? 'text-primary font-semibold'
                    : item.level === 0
                      ? 'text-text-secondary hover:text-text-main'
                      : 'text-text-muted hover:text-text-secondary'
                }`}
              >
                {item.text}
              </button>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
