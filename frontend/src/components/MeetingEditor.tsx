'use client';

import { useEffect, useRef, useCallback } from 'react';
import { useEditor, EditorContent } from '@tiptap/react';
import StarterKit from '@tiptap/starter-kit';
import Image from '@tiptap/extension-image';

interface MeetingEditorProps {
  content: string;
  onChange?: (content: string) => void;
  onAutoSave?: (content: string) => void;
  autoSaveDelay?: number;
  readOnly?: boolean;
}

export function MeetingEditor({
  content,
  onChange,
  onAutoSave,
  autoSaveDelay = 2000,
  readOnly = false,
}: MeetingEditorProps) {
  const autoSaveTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  const lastSavedContentRef = useRef(content);

  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        heading: {
          levels: [1, 2, 3],
        },
      }),
      Image.configure({
        inline: true,
        allowBase64: true,
      }),
    ],
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
