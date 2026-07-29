import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Dialogs, Events } from "@wailsio/runtime";
import { UploadService } from "../bindings/code.sli.ke/go/vega/apps/uploader-desktop";

// Upload mirrors ipc.UploadView (the daemon's wire shape). We keep a local type
// so the view never depends on generated-model resolution.
type Upload = {
  id: string;
  filename: string;
  size: number;
  status: string;
  bytesDone: number;
  curBps: number;
  avgBps: number;
  etaSeconds: number;
  error?: string;
  updatedAt: number;
};

const humanBytes = (n: number): string => {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
};

const humanBps = (n: number): string => (n > 0 ? `${humanBytes(n)}/s` : "—");

const humanETA = (s: number): string => {
  if (s <= 0) return "—";
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
};

// Which lifecycle states are actively transferring (so a Pause makes sense).
const running = new Set([
  "queued",
  "preparing",
  "ready",
  "connecting",
  "uploading",
  "reconnecting",
  "assembling",
  "verifying",
]);

const badgeClass = (status: string): string => {
  if (status === "completed") return "bg-emerald-500/15 text-emerald-300 ring-emerald-500/20";
  if (status === "failed") return "bg-rose-500/15 text-rose-300 ring-rose-500/20";
  if (status === "canceled") return "bg-zinc-500/15 text-zinc-400 ring-zinc-500/20";
  if (status === "paused") return "bg-amber-500/15 text-amber-300 ring-amber-500/20";
  return "bg-sky-500/15 text-sky-300 ring-sky-500/20";
};

const barClass = (status: string): string => {
  if (status === "completed") return "bg-emerald-400";
  if (status === "failed") return "bg-rose-400";
  if (status === "paused") return "bg-amber-400";
  return "bg-sky-400";
};

const UploadIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" className="h-8 w-8 stroke-zinc-500" strokeWidth={1.5}>
    <path d="M12 16V4m0 0 4 4m-4-4-4 4" strokeLinecap="round" strokeLinejoin="round" />
    <path d="M4 16v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

const PlusIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" className="h-4 w-4 stroke-current" strokeWidth={2}>
    <path d="M12 5v14M5 12h14" strokeLinecap="round" />
  </svg>
);

const PauseIcon = () => (
  <svg viewBox="0 0 24 24" fill="currentColor" className="h-4 w-4">
    <rect x="6" y="5" width="4" height="14" rx="1" />
    <rect x="14" y="5" width="4" height="14" rx="1" />
  </svg>
);

const PlayIcon = () => (
  <svg viewBox="0 0 24 24" fill="currentColor" className="h-4 w-4">
    <path d="M7 5.5v13l11-6.5-11-6.5Z" />
  </svg>
);

const CancelIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" className="h-4 w-4 stroke-current" strokeWidth={2}>
    <path d="M6 6l12 12M18 6 6 18" strokeLinecap="round" />
  </svg>
);

const FileIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" className="h-5 w-5 shrink-0 stroke-zinc-400" strokeWidth={1.5}>
    <path
      d="M6 3h7l5 5v11a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Z"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
    <path d="M13 3v5h5" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

function IconButton({
  onClick,
  className,
  title,
  children,
}: {
  onClick: () => void;
  className: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <button
      onClick={onClick}
      title={title}
      className={`flex h-7 w-7 items-center justify-center rounded-full text-zinc-400 transition hover:bg-white/10 active:scale-90 ${className}`}
    >
      {children}
    </button>
  );
}

function App() {
  const [uploads, setUploads] = useState<Upload[]>([]);
  const [connected, setConnected] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const list = (await UploadService.List()) as Upload[];
      setUploads(list ?? []);
      setConnected(true);
    } catch {
      setConnected(false);
    }
  }, []);

  useEffect(() => {
    refresh();
    // Live: the daemon pushes an event on every progress tick and lifecycle
    // change; we simply re-list. A slow poll covers any missed events.
    const off = Events.On("upload.event", () => refresh());
    const timer = setInterval(refresh, 1500);
    return () => {
      off();
      clearInterval(timer);
    };
  }, [refresh]);

  const addFiles = async () => {
    const picked = await Dialogs.OpenFile({
      Title: "Choose files to upload",
      CanChooseFiles: true,
      AllowsMultipleSelection: true,
    });
    for (const path of picked) {
      await UploadService.Enqueue(path);
    }
    refresh();
  };

  const act = (fn: (id: string) => PromiseLike<void>, id: string) => () =>
    Promise.resolve(fn(id)).then(refresh).catch(console.error);

  return (
    <div className="flex h-full flex-col text-zinc-100">
      <header className="flex items-center justify-between border-b border-white/5 px-8 pb-5 pt-14">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Slike Uploader</h1>
          <div className="mt-1 flex items-center gap-1.5 text-sm text-zinc-400">
            <span
              className={`h-1.5 w-1.5 rounded-full ${
                connected ? "bg-emerald-400 shadow-[0_0_6px_theme(colors.emerald.400)]" : "bg-zinc-600"
              }`}
            />
            {connected ? "Connected to upload engine" : "Upload engine not running"}
          </div>
        </div>
        <button
          onClick={addFiles}
          className="flex items-center gap-1.5 rounded-lg bg-sky-500 px-4 py-2 text-sm font-medium text-white shadow-lg shadow-sky-500/20 transition hover:bg-sky-400 active:scale-95"
        >
          <PlusIcon />
          Add files
        </button>
      </header>

      <main className="flex-1 space-y-2.5 overflow-y-auto px-8 py-6">
        {uploads.length === 0 && (
          <button
            onClick={addFiles}
            className="flex w-full flex-col items-center justify-center gap-3 rounded-2xl border border-dashed border-white/10 py-24 text-center transition hover:border-white/20 hover:bg-white/[0.02]"
          >
            <UploadIcon />
            <div>
              <p className="font-medium text-zinc-300">No uploads yet</p>
              <p className="mt-1 text-sm text-zinc-500">Click to choose files to upload</p>
            </div>
          </button>
        )}

        {uploads.map((u) => {
          const pct = u.size > 0 ? Math.min(100, (u.bytesDone / u.size) * 100) : 0;
          return (
            <div
              key={u.id}
              className="rounded-2xl border border-white/5 bg-white/[0.03] p-4 transition hover:border-white/10"
            >
              <div className="flex items-center justify-between gap-4">
                <div className="flex min-w-0 items-center gap-2.5">
                  <FileIcon />
                  <span className="truncate font-medium">{u.filename}</span>
                </div>
                <span
                  className={`shrink-0 rounded-full px-2.5 py-0.5 text-xs font-medium capitalize ring-1 ring-inset ${badgeClass(
                    u.status
                  )}`}
                >
                  {u.status}
                </span>
              </div>

              <div className="mt-3 h-1.5 w-full overflow-hidden rounded-full bg-white/5">
                <div
                  className={`h-full rounded-full transition-all duration-300 ${barClass(u.status)}`}
                  style={{ width: `${pct}%` }}
                />
              </div>

              <div className="mt-2.5 flex items-center justify-between text-xs text-zinc-400">
                <span>
                  {humanBytes(u.bytesDone)} / {humanBytes(u.size)}
                  {u.status === "uploading" && (
                    <> · {humanBps(u.avgBps)} · ETA {humanETA(u.etaSeconds)}</>
                  )}
                </span>
                <div className="flex gap-1">
                  {running.has(u.status) && (
                    <IconButton
                      title="Pause"
                      className="hover:text-amber-300"
                      onClick={act(UploadService.Pause, u.id)}
                    >
                      <PauseIcon />
                    </IconButton>
                  )}
                  {u.status === "paused" && (
                    <IconButton
                      title="Resume"
                      className="hover:text-sky-300"
                      onClick={act(UploadService.Resume, u.id)}
                    >
                      <PlayIcon />
                    </IconButton>
                  )}
                  {u.status !== "completed" && u.status !== "canceled" && (
                    <IconButton
                      title="Cancel"
                      className="hover:text-rose-300"
                      onClick={act(UploadService.Cancel, u.id)}
                    >
                      <CancelIcon />
                    </IconButton>
                  )}
                </div>
              </div>

              {u.error && <p className="mt-2 text-xs text-rose-300">{u.error}</p>}
            </div>
          );
        })}
      </main>
    </div>
  );
}

export default App;
