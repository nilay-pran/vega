import { useMemo, type ReactNode } from "react";
import {
  clockTime,
  dotClass,
  humanAgo,
  humanBps,
  humanBytes,
  kindOf,
  pctOf,
  running,
  STAGES,
  type LogLine,
  type Stats,
  type Upload,
} from "./format";
import {
  CheckIcon,
  ClockIcon,
  FilmIcon,
  FolderIcon,
  GridIcon,
  ListIcon,
  PauseIcon,
  PlayIcon,
  PlusIcon,
  PulseIcon,
  UploadCloudIcon,
} from "./icons";
import { EmptyState, Panel, StatTile, UploadRow, type RowActions } from "./ui";
import { SmallButton } from "./layout";

// Status filters offered on the Queue view, mapped onto daemon statuses.
const FILTERS: { key: string; label: string; match: (u: Upload) => boolean }[] = [
  { key: "all", label: "All", match: () => true },
  { key: "active", label: "Active", match: (u) => running.has(u.status) },
  { key: "paused", label: "Paused", match: (u) => u.status === "paused" },
  { key: "failed", label: "Failed", match: (u) => u.status === "failed" },
  { key: "completed", label: "Completed", match: (u) => u.status === "completed" },
];

export function QueueView({
  uploads,
  query,
  filter,
  setFilter,
  actions,
  stats,
  onPauseAll,
  onResumeAll,
  onGoIngest,
}: {
  uploads: Upload[];
  query: string;
  filter: string;
  setFilter: (f: string) => void;
  actions: RowActions;
  stats: Stats;
  onPauseAll: () => void;
  onResumeAll: () => void;
  onGoIngest: () => void;
}) {
  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    const f = FILTERS.find((x) => x.key === filter) ?? FILTERS[0];
    return uploads
      .filter(f.match)
      .filter((u) => !q || u.filename.toLowerCase().includes(q) || u.id.toLowerCase().includes(q))
      .sort((a, b) => b.updatedAt - a.updatedAt);
  }, [uploads, query, filter]);

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <div className="flex gap-1.5">
          {FILTERS.map((f) => {
            const n = uploads.filter(f.match).length;
            return (
              <button
                key={f.key}
                onClick={() => setFilter(f.key)}
                className={`rounded-full px-3 py-1.5 text-xs font-medium transition ${
                  filter === f.key
                    ? "bg-white/10 text-zinc-100"
                    : "text-zinc-400 hover:bg-white/5 hover:text-zinc-200"
                }`}
              >
                {f.label}
                <span className="ml-1.5 tabular-nums text-zinc-500">{n}</span>
              </button>
            );
          })}
        </div>
        <div className="flex gap-1.5">
          <SmallButton onClick={onPauseAll} disabled={stats.active.length === 0}>
            <PauseIcon className="h-3.5 w-3.5" />
            Pause all
          </SmallButton>
          <SmallButton onClick={onResumeAll} disabled={stats.paused.length + stats.failed.length === 0}>
            <PlayIcon className="h-3.5 w-3.5" />
            Resume all
          </SmallButton>
        </div>
      </div>

      {visible.length === 0 ? (
        <EmptyState
          icon={<ListIcon className="h-7 w-7" />}
          title={uploads.length === 0 ? "Queue is empty" : "Nothing matches this filter"}
          hint={
            uploads.length === 0
              ? "Add media from the Ingest screen to get started"
              : "Try another status filter or clear the search"
          }
          onClick={uploads.length === 0 ? onGoIngest : undefined}
        />
      ) : (
        <div className="space-y-2.5">
          {visible.map((u) => (
            <UploadRow key={u.id} u={u} actions={actions} />
          ))}
        </div>
      )}
    </div>
  );
}

export function IngestView({
  stats,
  uploads,
  onAdd,
  onAddFolder,
  onOpenQueue,
}: {
  stats: Stats;
  uploads: Upload[];
  onAdd: () => void;
  onAddFolder: () => void;
  onOpenQueue: () => void;
}) {
  const recent = useMemo(
    () => [...uploads].sort((a, b) => b.updatedAt - a.updatedAt).slice(0, 5),
    [uploads]
  );
  const overall = stats.bytesTotal > 0 ? (stats.bytesDone / stats.bytesTotal) * 100 : 0;

  return (
    <div className="space-y-5">
      {/* The drop zone: data-file-drop-target makes the Go side deliver Finder
          drops here, and the runtime toggles file-drop-target-active on hover. */}
      <div
        data-file-drop-target
        className="group relative overflow-hidden rounded-3xl border border-dashed border-white/12 bg-gradient-to-b from-white/[0.05] to-white/[0.015] transition [&.file-drop-target-active]:border-sky-400/60 [&.file-drop-target-active]:bg-sky-500/10"
      >
        <div className="pointer-events-none absolute -right-16 -top-24 h-64 w-64 rounded-full bg-sky-500/10 blur-3xl" />
        <div className="relative flex flex-col items-center px-8 py-11 text-center">
          <div className="grid h-14 w-14 place-items-center rounded-2xl border border-white/10 bg-white/[0.06]">
            <UploadCloudIcon className="h-7 w-7 stroke-sky-300" />
          </div>
          <h2 className="mt-4 text-lg font-semibold tracking-tight">Drop media to ingest</h2>
          <p className="mt-1 max-w-md text-sm text-zinc-400">
            Drag files from Finder, or browse. Every transfer is chunked and resumable, so a
            dropped connection picks up where it left off.
          </p>
          <div className="mt-5 flex gap-2">
            <button
              onClick={onAdd}
              className="flex items-center gap-1.5 rounded-lg bg-sky-500 px-4 py-2 text-sm font-medium text-white shadow-lg shadow-sky-500/20 transition hover:bg-sky-400 active:scale-95"
            >
              <PlusIcon className="h-4 w-4" />
              Browse files
            </button>
            <button
              onClick={onAddFolder}
              className="flex items-center gap-1.5 rounded-lg border border-white/10 bg-white/[0.05] px-4 py-2 text-sm font-medium text-zinc-200 transition hover:bg-white/10 active:scale-95"
            >
              <FolderIcon className="h-4 w-4" />
              Choose folder
            </button>
          </div>
          <div className="mt-6 flex flex-wrap justify-center gap-1.5">
            {["MP4", "MOV", "MKV", "MXF", "WebM", "Audio", "Subtitles"].map((t) => (
              <span
                key={t}
                className="rounded-full border border-white/5 bg-white/[0.04] px-2.5 py-1 text-[11px] font-medium text-zinc-400"
              >
                {t}
              </span>
            ))}
          </div>
        </div>
      </div>

      <div className="grid grid-cols-4 gap-3">
        <StatTile
          label="In queue"
          value={String(stats.inFlight.length)}
          sub={stats.paused.length > 0 ? `${stats.paused.length} paused` : "awaiting or in flight"}
          icon={<ClockIcon className="h-3.5 w-3.5 stroke-zinc-500" />}
        />
        <StatTile
          label="Uploading"
          value={String(stats.active.length)}
          sub={humanBps(stats.throughput)}
          accent={stats.active.length > 0 ? "text-sky-300" : "text-zinc-100"}
          icon={<UploadCloudIcon className="h-3.5 w-3.5 stroke-zinc-500" />}
        />
        <StatTile
          label="Completed"
          value={String(stats.done.length)}
          sub={humanBytes(stats.libraryBytes)}
          accent={stats.done.length > 0 ? "text-emerald-300" : "text-zinc-100"}
          icon={<CheckIcon className="h-3.5 w-3.5 stroke-zinc-500" />}
        />
        <StatTile
          label="Transferred"
          value={humanBytes(stats.bytesDone)}
          sub={stats.bytesTotal > 0 ? `${overall.toFixed(0)}% of ${humanBytes(stats.bytesTotal)}` : "nothing yet"}
          icon={<PulseIcon className="h-3.5 w-3.5 stroke-zinc-500" />}
        />
      </div>

      <div className="grid grid-cols-5 gap-3">
        <Panel title="Ingest pipeline" className="col-span-2">
          <ol className="space-y-3 px-4 py-4">
            {STAGES.map((s, i) => {
              const at = stats.inFlight.filter((u) =>
                (s.statuses as readonly string[]).includes(u.status)
              ).length;
              const busy = at > 0;
              return (
                <li key={s.key} className="flex items-center gap-3">
                  <span
                    className={`grid h-6 w-6 shrink-0 place-items-center rounded-full text-[11px] font-semibold ${
                      busy ? "bg-sky-500/20 text-sky-300 ring-1 ring-sky-400/30" : "bg-white/5 text-zinc-500"
                    }`}
                  >
                    {i + 1}
                  </span>
                  <span className={`flex-1 text-sm ${busy ? "text-zinc-100" : "text-zinc-400"}`}>
                    {s.label}
                  </span>
                  <span className="font-mono text-xs tabular-nums text-zinc-500">
                    {s.key === "done" ? stats.done.length : at}
                  </span>
                </li>
              );
            })}
          </ol>
          <p className="border-t border-white/5 px-4 py-2.5 text-[11px] text-zinc-500">
            Stages come from the engine's own upload lifecycle.
          </p>
        </Panel>

        <Panel
          title="Recent activity"
          className="col-span-3"
          action={
            <button
              onClick={onOpenQueue}
              className="text-[11px] font-medium text-sky-300 transition hover:text-sky-200"
            >
              Open queue →
            </button>
          }
        >
          {recent.length === 0 ? (
            <p className="px-4 py-10 text-center text-sm text-zinc-500">
              Nothing ingested yet in this workspace.
            </p>
          ) : (
            <ul className="divide-y divide-white/5">
              {recent.map((u) => (
                <li key={u.id} className="flex items-center gap-3 px-4 py-2.5">
                  <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dotClass(u.status)}`} />
                  <span className="min-w-0 flex-1 truncate text-sm text-zinc-200">{u.filename}</span>
                  <span className="shrink-0 text-xs capitalize text-zinc-400">{u.status}</span>
                  <span className="w-14 shrink-0 text-right font-mono text-xs tabular-nums text-zinc-500">
                    {pctOf(u).toFixed(0)}%
                  </span>
                  <span className="w-16 shrink-0 text-right text-xs text-zinc-500">
                    {humanAgo(u.updatedAt)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </div>
  );
}

export function LibraryView({
  uploads,
  query,
  bytes,
}: {
  uploads: Upload[];
  query: string;
  bytes: number;
}) {
  const q = query.trim().toLowerCase();
  const items = uploads
    .filter((u) => !q || u.filename.toLowerCase().includes(q))
    .sort((a, b) => b.updatedAt - a.updatedAt);

  if (items.length === 0)
    return (
      <EmptyState
        icon={<GridIcon className="h-7 w-7" />}
        title={uploads.length === 0 ? "No finished assets yet" : "No asset matches this search"}
        hint={
          uploads.length === 0
            ? "Completed uploads land here once the engine verifies them"
            : "Clear the search to see the whole library"
        }
      />
    );

  return (
    <div className="space-y-3">
      <p className="text-xs text-zinc-500">
        {items.length} asset{items.length === 1 ? "" : "s"} · {humanBytes(bytes)} uploaded
      </p>
      <div className="grid grid-cols-3 gap-3">
        {items.map((u) => (
          <article
            key={u.id}
            className="overflow-hidden rounded-2xl border border-white/5 bg-white/[0.03] transition hover:border-white/10"
          >
            <div className="relative grid aspect-video place-items-center bg-gradient-to-br from-white/[0.07] to-white/[0.01]">
              <FilmIcon className="h-7 w-7 stroke-zinc-600" />
              <span className="absolute left-2 top-2 rounded-full bg-emerald-500/15 px-2 py-0.5 text-[10px] font-medium text-emerald-300 ring-1 ring-inset ring-emerald-500/20">
                Uploaded
              </span>
              <span className="absolute bottom-2 right-2 rounded bg-black/50 px-1.5 py-0.5 text-[10px] font-medium text-zinc-300">
                {kindOf(u.filename)}
              </span>
            </div>
            <div className="px-3 py-2.5">
              <p className="truncate text-sm font-medium text-zinc-100">{u.filename}</p>
              <p className="mt-0.5 text-[11px] text-zinc-500">
                {humanBytes(u.size)} · {humanAgo(u.updatedAt)}
              </p>
            </div>
          </article>
        ))}
      </div>
    </div>
  );
}

export function ActivityView({ log }: { log: LogLine[] }) {
  if (log.length === 0)
    return (
      <EmptyState
        icon={<PulseIcon className="h-7 w-7" />}
        title="No events yet"
        hint="Lifecycle events from the engine appear here while the window is open"
      />
    );

  return (
    <Panel title={`Engine events · ${log.length}`}>
      <ul className="divide-y divide-white/5 font-mono text-xs">
        {log.map((l, i) => (
          <li key={i} className="flex gap-3 px-4 py-2">
            <span className="shrink-0 tabular-nums text-zinc-600">{clockTime(l.at)}</span>
            <span className="w-40 shrink-0 truncate text-sky-300">{l.kind}</span>
            <span className="w-40 shrink-0 truncate text-zinc-500">{l.uploadID || "—"}</span>
            <span className="min-w-0 flex-1 truncate text-zinc-400">{l.detail}</span>
          </li>
        ))}
      </ul>
    </Panel>
  );
}

export function SettingsView({
  connected,
  metrics,
  stats,
}: {
  connected: boolean;
  metrics: Record<string, number | undefined>;
  stats: Stats;
}) {
  const keys = Object.keys(metrics).sort();
  return (
    <div className="grid grid-cols-2 gap-3">
      <Panel title="Connection">
        <dl className="divide-y divide-white/5 text-sm">
          <Row label="Upload engine">
            <span className="flex items-center gap-1.5">
              <span className={`h-1.5 w-1.5 rounded-full ${connected ? "bg-emerald-400" : "bg-zinc-600"}`} />
              {connected ? "Connected" : "Not running"}
            </span>
          </Row>
          <Row label="Transport">Unix socket (per-user, token authed)</Row>
          <Row label="Ownership">Daemon owns state; UI is a controller</Row>
          <Row label="In flight">{stats.inFlight.length}</Row>
          <Row label="Throughput">{humanBps(stats.throughput)}</Row>
        </dl>
      </Panel>

      <Panel title="Engine counters">
        {keys.length === 0 ? (
          <p className="px-4 py-8 text-center text-sm text-zinc-500">
            {connected ? "No counters reported yet" : "Engine offline"}
          </p>
        ) : (
          <dl className="divide-y divide-white/5 text-sm">
            {keys.map((k) => (
              <Row key={k} label={k}>
                <span className="font-mono tabular-nums">{metrics[k]}</span>
              </Row>
            ))}
          </dl>
        )}
      </Panel>
    </div>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 px-4 py-2.5">
      <dt className="truncate text-zinc-400">{label}</dt>
      <dd className="shrink-0 text-zinc-200">{children}</dd>
    </div>
  );
}
