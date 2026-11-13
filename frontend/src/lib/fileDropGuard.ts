/**
 * Mac app only: the main window passes native file drops to the page
 * (`dragDropEnabled: false`), so a file dropped outside a drop zone would make the
 * WebView navigate away to it. Drop zones call `preventDefault` first; this blocks the
 * rest. Returns the cleanup.
 */
export function installStrayFileDropGuard(target: Pick<Window, 'addEventListener' | 'removeEventListener'>): () => void {
  const files = (event: globalThis.DragEvent) => Array.from(event.dataTransfer?.types ?? []).includes('Files');
  const onDragOver = (event: globalThis.DragEvent) => {
    if (!files(event) || event.defaultPrevented) return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'none';
  };
  const onDrop = (event: globalThis.DragEvent) => {
    if (files(event) && !event.defaultPrevented) event.preventDefault();
  };
  target.addEventListener('dragover', onDragOver as EventListener);
  target.addEventListener('drop', onDrop as EventListener);
  return () => {
    target.removeEventListener('dragover', onDragOver as EventListener);
    target.removeEventListener('drop', onDrop as EventListener);
  };
}
