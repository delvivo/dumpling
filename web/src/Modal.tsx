import { useEffect, useRef, useState, type ReactNode } from "react";

// Modal opens a native <dialog> with showModal(): centered, backdrop, focus moved inside and
// returned to the opener on close; Esc goes through onClose so React state stays the source of truth.
export default function Modal({ onClose, children }: { onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current!;
    const opener = document.activeElement as HTMLElement | null;
    d.showModal();
    d.querySelector<HTMLElement>("[data-autofocus]")?.focus();
    // React detaches the dialog before this cleanup runs, so native focus restore can't fire.
    return () => { d.close(); opener?.focus(); };
  }, []);
  return <dialog ref={ref} onCancel={(e) => { e.preventDefault(); onClose(); }}>{children}</dialog>;
}

// Confirm replaces window.confirm/prompt for destructive actions. With typeToConfirm, the button
// stays disabled until that exact text is typed (same gate as the restore wizard).
export function Confirm({ title, children, confirmLabel, typeToConfirm, onConfirm, onClose }: {
  title: string; children: ReactNode; confirmLabel: string; typeToConfirm?: string;
  onConfirm: (typed: string) => Promise<unknown> | void; onClose: () => void;
}) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const ok = typeToConfirm === undefined || typed === typeToConfirm;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try { await onConfirm(typed); } finally { onClose(); }
  };

  return (
    <Modal onClose={onClose}>
      <form onSubmit={submit}>
        <h2>{title}</h2>
        <div>{children}</div>
        {typeToConfirm !== undefined && (
          <label>Type <code>{typeToConfirm}</code> to confirm<input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" /></label>
        )}
        <div className="actions">
          <button className="danger" disabled={!ok || busy}>{confirmLabel}</button>
          {/* Safe default: Enter right after opening must not delete. */}
          <button type="button" onClick={onClose} data-autofocus={typeToConfirm === undefined || undefined}>Cancel</button>
        </div>
      </form>
    </Modal>
  );
}
