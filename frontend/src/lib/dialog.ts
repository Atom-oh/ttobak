import { isTauri } from '@/lib/tauri';

/**
 * Confirmation and alert dialogs that also work in the Mac app.
 *
 * The Mac app's WebView (wry/WKWebView) implements no JavaScript dialog
 * panels: `window.confirm()` returns false and `window.alert()` shows nothing,
 * so a confirmed action silently never runs there. Browsers keep their native
 * dialogs; the app gets an equivalent in-page dialog.
 */

type Button = { label: string; value: boolean; primary: boolean };

// Native dialogs are modal and serialize; in-page ones queue the same way,
// so only the topmost dialog ever owns the keyboard.
let queue: Promise<unknown> = Promise.resolve();

function showDialog(message: string, buttons: Button[]): Promise<boolean> {
  const run = queue.then(() => openDialog(message, buttons));
  queue = run.catch(() => undefined);
  return run;
}

function openDialog(message: string, buttons: Button[]): Promise<boolean> {
  return new Promise((resolve) => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overlay = document.createElement('div');
    overlay.className = 'fixed inset-0 z-[100] flex items-center justify-center bg-black/40 p-4';

    const panel = document.createElement('div');
    panel.setAttribute('role', 'alertdialog');
    panel.setAttribute('aria-modal', 'true');
    panel.className =
      'w-full max-w-sm rounded-xl border border-slate-200 dark:border-white/10 bg-white dark:bg-surface shadow-2xl p-5 flex flex-col gap-4';

    const text = document.createElement('p');
    text.id = `ttobak-dialog-${Date.now()}`;
    text.className = 'text-sm text-slate-800 dark:text-text-main whitespace-pre-wrap break-words';
    text.textContent = message;
    panel.setAttribute('aria-describedby', text.id);
    panel.appendChild(text);

    const row = document.createElement('div');
    row.className = 'flex justify-end gap-2';
    panel.appendChild(row);
    overlay.appendChild(panel);

    let settled = false;
    const finish = (value: boolean) => {
      if (settled) return;
      settled = true;
      document.removeEventListener('keydown', onKey, true);
      overlay.remove();
      previous?.focus();
      resolve(value);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        finish(false);
      } else if (e.key === 'Tab') {
        // Keep focus inside the dialog.
        const focusable = Array.from(row.querySelectorAll('button'));
        const index = focusable.indexOf(document.activeElement as HTMLButtonElement);
        const next = (index + (e.shiftKey ? -1 : 1) + focusable.length) % focusable.length;
        e.preventDefault();
        focusable[next]?.focus();
      }
    };

    let initial: HTMLButtonElement | null = null;
    for (const b of buttons) {
      const el = document.createElement('button');
      el.type = 'button';
      el.textContent = b.label;
      el.className = b.primary
        ? 'min-h-9 px-4 rounded-lg text-sm font-bold text-white bg-primary hover:bg-primary-hover'
        : 'min-h-9 px-4 rounded-lg text-sm text-slate-600 dark:text-text-secondary hover:bg-slate-100 dark:hover:bg-white/5';
      el.addEventListener('click', () => finish(b.value));
      row.appendChild(el);
      // Like the native confirm, a stray Enter must not run a destructive
      // action: focus starts on the non-primary button when there is one.
      if (!b.primary || !initial) initial = el;
    }

    document.addEventListener('keydown', onKey, true);
    document.body.appendChild(overlay);
    initial?.focus();
  });
}

/** Asks the user to confirm; resolves true only when they accept. */
export function confirmDialog(message: string): Promise<boolean> {
  if (typeof window === 'undefined') return Promise.resolve(false);
  if (!isTauri()) return Promise.resolve(window.confirm(message));
  return showDialog(message, [
    { label: '취소', value: false, primary: false },
    { label: '확인', value: true, primary: true },
  ]);
}

/** Shows a message the user acknowledges. */
export async function alertDialog(message: string): Promise<void> {
  if (typeof window === 'undefined') return;
  if (!isTauri()) {
    window.alert(message);
    return;
  }
  await showDialog(message, [{ label: '확인', value: true, primary: true }]);
}
