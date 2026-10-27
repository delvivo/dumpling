import { useEffect, useRef, type ReactNode } from "react";

// Modal opens a native <dialog> with showModal(): centered, backdrop, focus moved inside and
// returned to the opener on close; Esc goes through onClose so React state stays the source of truth.
export default function Modal({ onClose, children }: { onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current!;
    const opener = document.activeElement as HTMLElement | null;
    d.showModal();
    // React detaches the dialog before this cleanup runs, so native focus restore can't fire.
    return () => { d.close(); opener?.focus(); };
  }, []);
  return <dialog ref={ref} onCancel={(e) => { e.preventDefault(); onClose(); }}>{children}</dialog>;
}
