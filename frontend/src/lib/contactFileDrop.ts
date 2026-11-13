import { useRef, useState } from 'react';
import type { DragEvent } from 'react';

export const CONTACT_DROP_ONE_FILE = '파일은 한 번에 하나만 끌어다 놓을 수 있습니다.';

const hasFiles = (event: DragEvent) => Array.from(event.dataTransfer?.types ?? []).includes('Files');

/**
 * Drop-zone props for the Remember/CSV/Excel contact import inputs. Only file drags
 * are handled; a drop while `disabled` is swallowed so the page never navigates to
 * the file. File type and size checks stay in `readContactFile`.
 */
export function useContactFileDrop({ disabled, onFile, onReject }: {
  disabled: boolean;
  onFile: (file: File) => void;
  onReject: (message: string) => void;
}) {
  const [dragging, setDragging] = useState(false);
  const depth = useRef(0);
  const reset = () => { depth.current = 0; setDragging(false); };
  const zoneProps = {
    onDragEnter: (event: DragEvent) => {
      if (!hasFiles(event)) return;
      event.preventDefault();
      depth.current += 1;
      if (!disabled) setDragging(true);
    },
    onDragOver: (event: DragEvent) => {
      if (!hasFiles(event)) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = disabled ? 'none' : 'copy';
    },
    onDragLeave: (event: DragEvent) => {
      if (!hasFiles(event)) return;
      depth.current = Math.max(0, depth.current - 1);
      if (depth.current === 0) setDragging(false);
    },
    onDrop: (event: DragEvent) => {
      if (!hasFiles(event)) return;
      event.preventDefault();
      reset();
      if (disabled) return;
      const files = Array.from(event.dataTransfer.files ?? []);
      if (files.length !== 1) { onReject(CONTACT_DROP_ONE_FILE); return; }
      onFile(files[0]);
    },
  };
  return { dragging, zoneProps };
}
