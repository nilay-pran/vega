import type { ReactNode } from "react";
import {
  CancelIcon,
  FilmIcon,
  PauseIcon,
  PlayIcon,
  RetryIcon,
} from "./icons";
import {
  badgeClass,
  barClass,
  humanAgo,
  humanBps,
  humanBytes,
  humanETA,
  isTerminal,
  kindOf,
  pctOf,
  running,
  STAGES,
  stageIndexOf,
  type Upload,
} from "./format";

export function IconButton({
  onClick,
  className = "",
  title,
  children,
}: {
  onClick: () => void;
  className?: string;
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

export function Panel({
  title,
  action,
  children,
  className = "",
}: {
  title?: string;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section
      className={`rounded-2xl border border-white/5 bg-white/[0.025] ${className}`}
    >
      {title && (
        <header className="flex items-center justify-between border-b border-white/5 px-4 py-3">
          <h2 className="text-[13px] font-semibold tracking-wide text-zinc-300">{title}</h2>
          {action}
        </header>
      )}
      {children}
    </section>
  );
}

export function StatTile({
  label,
  value,
  sub,
  accent = "text-zinc-100",
  icon,
}: {
  label: string;
  value: string;
  sub?: string;
  accent?: string;
  icon?: ReactNode;
}) {
  return (
    <div className="rounded-2xl border border-white/5 bg-white/[0.025] px-4 py-3.5">
      <div className="flex items-center justify-between text-[11px] font-medium uppercase tracking-wider text-zinc-500">
        {label}
        {icon}
      </div>
      <div className={`mt-2 text-2xl font-semibold tabular-nums tracking-tight ${accent}`}>
        {value}
      </div>
      <div className="mt-0.5 h-4 text-xs text-zinc-500">{sub ?? ""}</div>
    </div>
  );
}

// StageTrack renders the daemon lifecycle as a five-step pipeline, with the
// current status lighting up its step. Non-pipeline states (paused, failed)
// leave every step dim.
export function StageTrack({ status }: { status: string }) {
  const at = stageIndexOf(status);
  return (
    <div className="flex items-center gap-1">
      {STAGES.map((s, i) => {
        const done = at >= 0 && i < at;
        const here = i === at;
        return (
          <div key={s.key} className="flex items-center gap-1">
            <span
              title={s.label}
              className={`h-1 w-6 rounded-full transition-colors ${
                here
                  ? status === "completed"
                    ? "bg-emerald-400"
                    : "bg-sky-400"
                  : done
                    ? "bg-sky-400/40"
                    : "bg-white/10"
              }`}
            />
          </div>
        );
      })}
      <span className="ml-1.5 text-[11px] text-zinc-500">
        {at >= 0 ? STAGES[at].label : "—"}
      </span>
    </div>
  );
}

export type RowActions = {
  pause: (id: string) => () => void;
  resume: (id: string) => () => void;
  cancel: (id: string) => () => void;
};

// UploadRow is the CMS-style asset row: poster tile, title + technical meta,
// pipeline track, progress bar and the lifecycle actions.
export function UploadRow({
  u,
  actions,
  compact = false,
}: {
  u: Upload;
  actions: RowActions;
  compact?: boolean;
}) {
  const pct = pctOf(u);
  const active = u.status === "uploading" || u.status === "reconnecting";
  return (
    <div className="group rounded-2xl border border-white/5 bg-white/[0.03] p-3.5 transition hover:border-white/10 hover:bg-white/[0.045]">
      <div className="flex items-start gap-3.5">
        <div className="relative grid h-14 w-24 shrink-0 place-items-center overflow-hidden rounded-lg border border-white/5 bg-gradient-to-br from-white/[0.07] to-white/[0.01]">
          <FilmIcon className="h-5 w-5 stroke-zinc-500" />
          <span className="absolute bottom-0 right-0 rounded-tl-md bg-black/50 px-1 py-0.5 text-[10px] font-medium text-zinc-300">
            {kindOf(u.filename)}
          </span>
        </div>

        <div className="min-w-0 flex-1">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <p className="truncate text-sm font-medium text-zinc-100">{u.filename}</p>
              <p className="mt-0.5 truncate font-mono text-[11px] text-zinc-500">
                {u.id} · {humanBytes(u.size)} · {humanAgo(u.updatedAt)}
              </p>
            </div>
            <div className="flex shrink-0 items-center gap-1.5">
              <span
                className={`rounded-full px-2.5 py-0.5 text-xs font-medium capitalize ring-1 ring-inset ${badgeClass(
                  u.status
                )}`}
              >
                {u.status}
              </span>
              <div className="flex gap-0.5 opacity-60 transition group-hover:opacity-100">
                {running.has(u.status) && (
                  <IconButton
                    title="Pause"
                    className="hover:text-amber-300"
                    onClick={actions.pause(u.id)}
                  >
                    <PauseIcon />
                  </IconButton>
                )}
                {u.status === "paused" && (
                  <IconButton
                    title="Resume"
                    className="hover:text-sky-300"
                    onClick={actions.resume(u.id)}
                  >
                    <PlayIcon />
                  </IconButton>
                )}
                {u.status === "failed" && (
                  <IconButton
                    title="Resume (retry)"
                    className="hover:text-sky-300"
                    onClick={actions.resume(u.id)}
                  >
                    <RetryIcon />
                  </IconButton>
                )}
                {!isTerminal(u.status) && (
                  <IconButton
                    title="Cancel"
                    className="hover:text-rose-300"
                    onClick={actions.cancel(u.id)}
                  >
                    <CancelIcon />
                  </IconButton>
                )}
              </div>
            </div>
          </div>

          <div className="mt-2.5 h-1.5 w-full overflow-hidden rounded-full bg-white/5">
            <div
              className={`h-full rounded-full transition-all duration-300 ${barClass(u.status)}`}
              style={{ width: `${pct}%` }}
            />
          </div>

          <div className="mt-2 flex items-center justify-between gap-4 text-xs text-zinc-400">
            <span className="tabular-nums">
              {pct.toFixed(0)}% · {humanBytes(u.bytesDone)} / {humanBytes(u.size)}
              {active && <> · {humanBps(u.curBps)} · ETA {humanETA(u.etaSeconds)}</>}
            </span>
            {!compact && <StageTrack status={u.status} />}
          </div>

          {u.error && (
            <p className="mt-2 rounded-lg bg-rose-500/10 px-2.5 py-1.5 text-xs text-rose-300">
              {u.error}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}

export function EmptyState({
  title,
  hint,
  icon,
  onClick,
}: {
  title: string;
  hint: string;
  icon: ReactNode;
  onClick?: () => void;
}) {
  const inner = (
    <>
      <div className="text-zinc-500">{icon}</div>
      <div>
        <p className="font-medium text-zinc-300">{title}</p>
        <p className="mt-1 text-sm text-zinc-500">{hint}</p>
      </div>
    </>
  );
  const base =
    "flex w-full flex-col items-center justify-center gap-3 rounded-2xl border border-dashed border-white/10 py-16 text-center";
  return onClick ? (
    <button onClick={onClick} className={`${base} transition hover:border-white/20 hover:bg-white/[0.02]`}>
      {inner}
    </button>
  ) : (
    <div className={base}>{inner}</div>
  );
}
