import type { ReactNode } from "react";
import { humanBps } from "./format";
import { GearIcon, GridIcon, ListIcon, PlusIcon, PulseIcon, SearchIcon, UploadCloudIcon } from "./icons";

export type View = "ingest" | "queue" | "library" | "activity" | "settings";

const NAV: { key: View; label: string; icon: (p: { className?: string }) => ReactNode }[] = [
  { key: "ingest", label: "Ingest", icon: UploadCloudIcon },
  { key: "queue", label: "Queue", icon: ListIcon },
  { key: "library", label: "Library", icon: GridIcon },
  { key: "activity", label: "Activity", icon: PulseIcon },
  { key: "settings", label: "Engine", icon: GearIcon },
];

const TITLES: Record<View, { title: string; sub: string }> = {
  ingest: { title: "Ingest", sub: "Bring new media into the CMS" },
  queue: { title: "Upload queue", sub: "Live transfer state, straight from the engine" },
  library: { title: "Library", sub: "Assets that finished uploading" },
  activity: { title: "Activity", sub: "Engine event stream for this session" },
  settings: { title: "Engine", sub: "Daemon connection and counters" },
};

export function Sidebar({
  view,
  setView,
  connected,
  counts,
  throughput,
}: {
  view: View;
  setView: (v: View) => void;
  connected: boolean;
  counts: { queue: number; library: number; activity: number };
  throughput: number;
}) {
  const badge = (key: View) =>
    key === "queue" ? counts.queue : key === "library" ? counts.library : key === "activity" ? counts.activity : 0;

  return (
    <aside className="flex w-[212px] shrink-0 flex-col border-r border-white/5 bg-black/20">
      <div className="flex items-center gap-2.5 px-5 pb-4 pt-14">
        <div className="grid h-8 w-8 place-items-center rounded-lg bg-gradient-to-br from-sky-400 to-indigo-500 text-sm font-bold text-white shadow-lg shadow-sky-500/20">
          S
        </div>
        <div className="leading-tight">
          <p className="text-sm font-semibold tracking-tight">Slike Studio</p>
          <p className="text-[11px] text-zinc-500">Video CMS · Ingest</p>
        </div>
      </div>

      <nav className="space-y-0.5 px-2.5">
        {NAV.map(({ key, label, icon: Icon }) => {
          const on = view === key;
          const n = badge(key);
          return (
            <button
              key={key}
              onClick={() => setView(key)}
              className={`flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm transition ${
                on
                  ? "bg-white/10 font-medium text-zinc-100"
                  : "text-zinc-400 hover:bg-white/5 hover:text-zinc-200"
              }`}
            >
              <Icon className={`h-4 w-4 ${on ? "stroke-sky-300" : ""}`} />
              <span className="flex-1 text-left">{label}</span>
              {n > 0 && (
                <span className="rounded-full bg-white/10 px-1.5 py-0.5 text-[10px] font-medium tabular-nums text-zinc-300">
                  {n}
                </span>
              )}
            </button>
          );
        })}
      </nav>

      <div className="mt-auto space-y-2 px-4 pb-5">
        <div className="rounded-xl border border-white/5 bg-white/[0.03] px-3 py-2.5">
          <div className="flex items-center gap-1.5 text-xs">
            <span
              className={`h-1.5 w-1.5 rounded-full ${
                connected ? "bg-emerald-400 shadow-[0_0_6px_theme(colors.emerald.400)]" : "bg-zinc-600"
              }`}
            />
            <span className={connected ? "text-zinc-300" : "text-zinc-500"}>
              {connected ? "Engine online" : "Engine offline"}
            </span>
          </div>
          <p className="mt-1.5 font-mono text-[11px] tabular-nums text-zinc-500">
            {throughput > 0 ? `↑ ${humanBps(throughput)}` : "idle"}
          </p>
        </div>
        <p className="px-1 text-[10px] text-zinc-600">
          Uploads run in the background daemon — closing this window keeps them going.
        </p>
      </div>
    </aside>
  );
}

export function TopBar({
  view,
  query,
  setQuery,
  onAdd,
  uploads,
}: {
  view: View;
  query: string;
  setQuery: (v: string) => void;
  onAdd: () => void;
  uploads: number;
}) {
  const searchable = view === "queue" || view === "library";
  return (
    <header className="flex items-end justify-between gap-4 px-7 pb-5 pt-14">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">{TITLES[view].title}</h1>
        <p className="mt-0.5 text-sm text-zinc-500">{TITLES[view].sub}</p>
      </div>
      <div className="flex items-center gap-2">
        {searchable && (
          <div className="relative">
            <SearchIcon className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 stroke-zinc-500" />
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={`Search ${uploads} asset${uploads === 1 ? "" : "s"}…`}
              className="w-56 rounded-lg border border-white/5 bg-white/[0.04] py-2 pl-8 pr-3 text-sm text-zinc-200 placeholder:text-zinc-500 focus:border-sky-500/40 focus:outline-none"
            />
          </div>
        )}
        <button
          onClick={onAdd}
          className="flex items-center gap-1.5 rounded-lg bg-sky-500 px-4 py-2 text-sm font-medium text-white shadow-lg shadow-sky-500/20 transition hover:bg-sky-400 active:scale-95"
        >
          <PlusIcon className="h-4 w-4" />
          Add media
        </button>
      </div>
    </header>
  );
}

export function SmallButton({
  onClick,
  disabled,
  children,
}: {
  onClick: () => void;
  disabled?: boolean;
  children: ReactNode;
}) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      className="flex items-center gap-1.5 rounded-lg border border-white/5 bg-white/[0.04] px-2.5 py-1.5 text-xs font-medium text-zinc-300 transition hover:bg-white/[0.08] disabled:cursor-not-allowed disabled:opacity-40"
    >
      {children}
    </button>
  );
}
