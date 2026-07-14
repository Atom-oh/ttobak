'use client';

import { useEffect, useRef, useCallback, useMemo } from 'react';
import { useEditor, EditorContent } from '@tiptap/react';
import { Extension } from '@tiptap/core';
import StarterKit from '@tiptap/starter-kit';
import Image from '@tiptap/extension-image';
import Suggestion from '@tiptap/suggestion';

// Triggered by "[[" -- suggests existing document titles for a wikilink.
// getTitles lives on the extension's own `options` (a plain mutable object,
// not a React ref) so it can be swapped out imperatively from an effect --
// reading it happens inside `items()`, a callback the Suggestion plugin
// invokes on keystroke, never during render.
function createWikilinkExtension() {
  return Extension.create({
    name: 'wikilinkSuggestion',
    addOptions() {
      return { getTitles: () => [] as string[] };
    },
    addProseMirrorPlugins() {
      return [
        Suggestion({
          editor: this.editor,
          char: '[[',
          allowSpaces: true,
          items: ({ query }) => {
            const titles: string[] = this.options.getTitles();
            const filtered = query
              ? titles.filter((t: string) => t.toLowerCase().includes(query.toLowerCase()))
              : titles;
            return filtered.slice(0, 8);
          },
          // Plain text insert: turndown round-trips "[[제목]]" back out as
          // literal markdown, no custom node/mark needed.
          command: ({ editor, range, props }) => {
            editor.chain().focus().insertContentAt(range, `${props}]]`).run();
          },
          render: () => {
            let el: HTMLDivElement | null = null;
            let unmount: (() => void) | null = null;
            let items: string[] = [];
            let selected = 0;
            let onPick: ((item: string) => void) | null = null;

            const draw = () => {
              if (!el) return;
              el.innerHTML = '';
              items.forEach((item, i) => {
                const row = document.createElement('div');
                row.textContent = item;
                row.className =
                  'px-3 py-1.5 text-sm cursor-pointer' +
                  (i === selected
                    ? ' bg-slate-100 dark:bg-slate-700'
                    : ' hover:bg-slate-50 dark:hover:bg-slate-800');
                row.addEventListener('mousedown', (e) => {
                  e.preventDefault();
                  onPick?.(item);
                });
                el!.appendChild(row);
              });
            };

            return {
              onStart: (p) => {
                items = p.items;
                selected = 0;
                onPick = (item) => p.command(item);
                el = document.createElement('div');
                el.className =
                  'z-50 min-w-[160px] max-h-56 overflow-y-auto rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 shadow-lg py-1';
                draw();
                unmount = p.mount(el);
              },
              onUpdate: (p) => {
                items = p.items;
                selected = 0;
                onPick = (item) => p.command(item);
                draw();
              },
              onKeyDown: ({ event }) => {
                if (items.length === 0) return false;
                if (event.key === 'ArrowDown') {
                  selected = (selected + 1) % items.length;
                  draw();
                  return true;
                }
                if (event.key === 'ArrowUp') {
                  selected = (selected - 1 + items.length) % items.length;
                  draw();
                  return true;
                }
                if (event.key === 'Enter') {
                  onPick?.(items[selected]);
                  return true;
                }
                return false;
              },
              onExit: () => {
                unmount?.();
                el = null;
              },
            };
          },
        }),
      ];
    },
  });
}

interface MeetingEditorProps {
  content: string;
  onChange?: (content: string) => void;
  onAutoSave?: (content: string) => void;
  autoSaveDelay?: number;
  readOnly?: boolean;
  /** Enables "[[" wikilink autocomplete, suggesting from this title list. */
  wikilinkTitles?: string[];
}

export function MeetingEditor({
  content,
  onChange,
  onAutoSave,
  autoSaveDelay = 2000,
  readOnly = false,
  wikilinkTitles,
}: MeetingEditorProps) {
  const autoSaveTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  const lastSavedContentRef = useRef(content);
  const wikilinkTitlesRef = useRef<string[]>(wikilinkTitles ?? []);
  useEffect(() => {
    wikilinkTitlesRef.current = wikilinkTitles ?? [];
  }, [wikilinkTitles]);

  // wikilinkTitles' presence (not its contents) decides whether the
  // extension is registered -- it's read fresh via the ref above, so the
  // caller's list can populate asynchronously after mount.
  const hasWikilinks = wikilinkTitles !== undefined;
  const extensions = useMemo(() => {
    const base = [
      StarterKit.configure({
        heading: {
          levels: [1, 2, 3],
        },
      }),
      Image.configure({
        inline: true,
        allowBase64: true,
      }),
    ];
    return hasWikilinks ? [...base, createWikilinkExtension()] : base;
  }, [hasWikilinks]);

  const editor = useEditor({
    extensions,
    content,
    editable: !readOnly,
    onUpdate: ({ editor }) => {
      const html = editor.getHTML();
      onChange?.(html);

      // Auto-save with debounce
      if (onAutoSave && html !== lastSavedContentRef.current) {
        if (autoSaveTimeoutRef.current) {
          clearTimeout(autoSaveTimeoutRef.current);
        }
        autoSaveTimeoutRef.current = setTimeout(() => {
          onAutoSave(html);
          lastSavedContentRef.current = html;
        }, autoSaveDelay);
      }
    },
    editorProps: {
      attributes: {
        class:
          'prose prose-slate dark:prose-invert max-w-none focus:outline-none min-h-[200px] px-4 py-3',
      },
    },
  });

  // Update content when prop changes
  useEffect(() => {
    if (editor && content !== editor.getHTML()) {
      editor.commands.setContent(content);
    }
  }, [content, editor]);

  // Wire the wikilink extension's getTitles to always read the latest ref
  // value. Runs once per editor instance (not per wikilinkTitles change) --
  // the ref indirection is what keeps the suggestion list current.
  useEffect(() => {
    if (!editor || !hasWikilinks) return;
    const ext = editor.extensionManager.extensions.find((e) => e.name === 'wikilinkSuggestion');
    if (ext) ext.options.getTitles = () => wikilinkTitlesRef.current;
  }, [editor, hasWikilinks]);

  // Cleanup
  useEffect(() => {
    return () => {
      if (autoSaveTimeoutRef.current) {
        clearTimeout(autoSaveTimeoutRef.current);
      }
    };
  }, []);

  const addImage = useCallback(() => {
    const url = prompt('Enter image URL');
    if (url && editor) {
      editor.chain().focus().setImage({ src: url }).run();
    }
  }, [editor]);

  if (!editor) {
    return (
      <div className="animate-pulse bg-slate-100 dark:bg-slate-800 rounded-xl h-64" />
    );
  }

  return (
    <div className="border border-slate-200 dark:border-slate-700 rounded-xl overflow-hidden bg-white dark:bg-slate-900">
      {/* Toolbar */}
      {!readOnly && (
        <div className="flex items-center gap-1 px-3 py-2 border-b border-slate-200 dark:border-slate-700 bg-slate-50 dark:bg-slate-800/50 flex-wrap">
          <button
            onClick={() => editor.chain().focus().toggleBold().run()}
            className={`p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors ${
              editor.isActive('bold') ? 'bg-slate-200 dark:bg-slate-700' : ''
            }`}
            title="Bold"
          >
            <span className="material-symbols-outlined text-lg">format_bold</span>
          </button>
          <button
            onClick={() => editor.chain().focus().toggleItalic().run()}
            className={`p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors ${
              editor.isActive('italic') ? 'bg-slate-200 dark:bg-slate-700' : ''
            }`}
            title="Italic"
          >
            <span className="material-symbols-outlined text-lg">format_italic</span>
          </button>
          <button
            onClick={() => editor.chain().focus().toggleStrike().run()}
            className={`p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors ${
              editor.isActive('strike') ? 'bg-slate-200 dark:bg-slate-700' : ''
            }`}
            title="Strikethrough"
          >
            <span className="material-symbols-outlined text-lg">strikethrough_s</span>
          </button>

          <div className="w-px h-5 bg-slate-300 dark:bg-slate-600 mx-1" />

          <button
            onClick={() => editor.chain().focus().toggleHeading({ level: 1 }).run()}
            className={`p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors ${
              editor.isActive('heading', { level: 1 }) ? 'bg-slate-200 dark:bg-slate-700' : ''
            }`}
            title="Heading 1"
          >
            <span className="text-sm font-bold">H1</span>
          </button>
          <button
            onClick={() => editor.chain().focus().toggleHeading({ level: 2 }).run()}
            className={`p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors ${
              editor.isActive('heading', { level: 2 }) ? 'bg-slate-200 dark:bg-slate-700' : ''
            }`}
            title="Heading 2"
          >
            <span className="text-sm font-bold">H2</span>
          </button>
          <button
            onClick={() => editor.chain().focus().toggleHeading({ level: 3 }).run()}
            className={`p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors ${
              editor.isActive('heading', { level: 3 }) ? 'bg-slate-200 dark:bg-slate-700' : ''
            }`}
            title="Heading 3"
          >
            <span className="text-sm font-bold">H3</span>
          </button>

          <div className="w-px h-5 bg-slate-300 dark:bg-slate-600 mx-1" />

          <button
            onClick={() => editor.chain().focus().toggleBulletList().run()}
            className={`p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors ${
              editor.isActive('bulletList') ? 'bg-slate-200 dark:bg-slate-700' : ''
            }`}
            title="Bullet List"
          >
            <span className="material-symbols-outlined text-lg">format_list_bulleted</span>
          </button>
          <button
            onClick={() => editor.chain().focus().toggleOrderedList().run()}
            className={`p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors ${
              editor.isActive('orderedList') ? 'bg-slate-200 dark:bg-slate-700' : ''
            }`}
            title="Numbered List"
          >
            <span className="material-symbols-outlined text-lg">format_list_numbered</span>
          </button>

          <div className="w-px h-5 bg-slate-300 dark:bg-slate-600 mx-1" />

          <button
            onClick={() => editor.chain().focus().toggleBlockquote().run()}
            className={`p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors ${
              editor.isActive('blockquote') ? 'bg-slate-200 dark:bg-slate-700' : ''
            }`}
            title="Quote"
          >
            <span className="material-symbols-outlined text-lg">format_quote</span>
          </button>
          <button
            onClick={() => editor.chain().focus().toggleCodeBlock().run()}
            className={`p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors ${
              editor.isActive('codeBlock') ? 'bg-slate-200 dark:bg-slate-700' : ''
            }`}
            title="Code Block"
          >
            <span className="material-symbols-outlined text-lg">code</span>
          </button>

          <div className="w-px h-5 bg-slate-300 dark:bg-slate-600 mx-1" />

          <button
            onClick={addImage}
            className="p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors"
            title="Insert Image"
          >
            <span className="material-symbols-outlined text-lg">image</span>
          </button>

          <div className="flex-1" />

          <button
            onClick={() => editor.chain().focus().undo().run()}
            disabled={!editor.can().undo()}
            className="p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors disabled:opacity-30"
            title="Undo"
          >
            <span className="material-symbols-outlined text-lg">undo</span>
          </button>
          <button
            onClick={() => editor.chain().focus().redo().run()}
            disabled={!editor.can().redo()}
            className="p-1.5 rounded hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors disabled:opacity-30"
            title="Redo"
          >
            <span className="material-symbols-outlined text-lg">redo</span>
          </button>
        </div>
      )}

      {/* Editor Content */}
      <EditorContent editor={editor} />
    </div>
  );
}
