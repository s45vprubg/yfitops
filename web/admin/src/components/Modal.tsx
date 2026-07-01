import { createContext, useCallback, useContext, useRef, useState } from "react";

// Modal — an in-app replacement for the native window.confirm / window.prompt,
// styled to the control room. Exposed as async functions via a context hook so
// call sites read almost like the natives they replace:
//
//   const { confirm, promptText } = useModal();
//   if (await confirm({ title: "End game?", danger: true })) { ... }
//   const name = await promptText({ title: "Category name" });  // string | null
//
// A single dialog instance is rendered at the app root by <ModalProvider>.

type ConfirmOpts = {
  title: string;
  body?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
};

type PromptOpts = {
  title: string;
  body?: string;
  placeholder?: string;
  defaultValue?: string;
  confirmLabel?: string;
  cancelLabel?: string;
};

interface ModalApi {
  confirm: (opts: ConfirmOpts) => Promise<boolean>;
  promptText: (opts: PromptOpts) => Promise<string | null>;
}

const ModalContext = createContext<ModalApi | null>(null);

export function useModal(): ModalApi {
  const ctx = useContext(ModalContext);
  if (!ctx) throw new Error("useModal must be used within <ModalProvider>");
  return ctx;
}

type Kind = "confirm" | "prompt";
interface DialogState {
  kind: Kind;
  opts: ConfirmOpts & PromptOpts;
  value: string;
}

export function ModalProvider({ children }: { children: React.ReactNode }) {
  const [dialog, setDialog] = useState<DialogState | null>(null);
  // The pending promise resolver for the open dialog.
  const resolver = useRef<((v: unknown) => void) | null>(null);

  const close = useCallback((result: unknown) => {
    resolver.current?.(result);
    resolver.current = null;
    setDialog(null);
  }, []);

  const confirm = useCallback((opts: ConfirmOpts) => {
    return new Promise<boolean>((resolve) => {
      resolver.current = resolve as (v: unknown) => void;
      setDialog({ kind: "confirm", opts, value: "" });
    });
  }, []);

  const promptText = useCallback((opts: PromptOpts) => {
    return new Promise<string | null>((resolve) => {
      resolver.current = resolve as (v: unknown) => void;
      setDialog({ kind: "prompt", opts, value: opts.defaultValue ?? "" });
    });
  }, []);

  return (
    <ModalContext.Provider value={{ confirm, promptText }}>
      {children}
      {dialog && (
        <div
          className="fixed inset-0 z-[60] flex items-center justify-center bg-black/70 backdrop-blur-sm"
          onMouseDown={(e) => {
            // Click outside = cancel.
            if (e.target === e.currentTarget) close(dialog.kind === "prompt" ? null : false);
          }}
        >
          <div className="w-[26rem] max-w-[90vw] rounded-lg border border-edge bg-panel2 p-5 shadow-xl">
            <div className="mb-2 text-sm font-bold uppercase tracking-[0.15em] text-accent">
              {dialog.opts.title}
            </div>
            {dialog.opts.body && (
              <div className="mb-4 text-sm leading-snug text-slate-300">{dialog.opts.body}</div>
            )}
            {dialog.kind === "prompt" && (
              <input
                autoFocus
                value={dialog.value}
                placeholder={dialog.opts.placeholder}
                onChange={(e) => setDialog((d) => (d ? { ...d, value: e.target.value } : d))}
                onKeyDown={(e) => {
                  if (e.key === "Enter") close(dialog.value.trim() || null);
                  if (e.key === "Escape") close(null);
                }}
                className="mb-4 w-full rounded border border-edge bg-panel px-3 py-2 text-sm text-slate-100 outline-none focus:border-accent"
              />
            )}
            <div className="flex justify-end gap-2">
              <button
                onClick={() => close(dialog.kind === "prompt" ? null : false)}
                className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-slate-300 hover:text-white"
              >
                {dialog.opts.cancelLabel ?? "Cancel"}
              </button>
              <button
                autoFocus={dialog.kind === "confirm"}
                onClick={() => close(dialog.kind === "prompt" ? dialog.value.trim() || null : true)}
                className={[
                  "rounded border px-3 py-1.5 text-xs font-bold uppercase",
                  dialog.opts.danger
                    ? "border-red-800 bg-red-950/40 text-red-300 hover:bg-red-900/50"
                    : "border-accent/60 bg-accent/10 text-accent hover:bg-accent/20",
                ].join(" ")}
              >
                {dialog.opts.confirmLabel ?? "OK"}
              </button>
            </div>
          </div>
        </div>
      )}
    </ModalContext.Provider>
  );
}
