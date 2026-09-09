// Upload mirrors ipc.UploadView (the daemon's wire shape). We keep a local type
// so the view never depends on generated-model resolution.
export type Upload = {
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

export type LogLine = { at: Date; kind: string; uploadID: string; detail: string };

export type Stats = {
  active: Upload[];
  done: Upload[];
  paused: Upload[];
  failed: Upload[];
  inFlight: Upload[];
  throughput: number;
  bytesDone: number;
  bytesTotal: number;
  libraryBytes: number;
};

export const humanBytes = (n: number): string => {
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

export const humanBps = (n: number): string => (n > 0 ? `${humanBytes(n)}/s` : "—");

export const humanETA = (s: number): string => {
  if (s <= 0) return "—";
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
};

// updatedAt is a Unix seconds stamp from the daemon.
export const humanAgo = (unixSeconds: number): string => {
  if (!unixSeconds) return "—";
  const s = Math.max(0, Math.floor(Date.now() / 1000) - unixSeconds);
  if (s < 10) return "just now";
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
};

export const clockTime = (d: Date): string =>
  d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });

// Which lifecycle states are actively transferring (so a Pause makes sense).
export const running = new Set([
  "queued",
  "preparing",
  "ready",
  "connecting",
  "uploading",
  "reconnecting",
  "assembling",
  "verifying",
]);

export const isTerminal = (status: string): boolean =>
  status === "completed" || status === "canceled" || status === "failed";

export const pctOf = (u: Upload): number =>
  u.size > 0 ? Math.min(100, (u.bytesDone / u.size) * 100) : 0;

export const badgeClass = (status: string): string => {
  if (status === "completed") return "bg-emerald-500/15 text-emerald-300 ring-emerald-500/20";
  if (status === "failed") return "bg-rose-500/15 text-rose-300 ring-rose-500/20";
  if (status === "canceled") return "bg-zinc-500/15 text-zinc-400 ring-zinc-500/20";
  if (status === "paused") return "bg-amber-500/15 text-amber-300 ring-amber-500/20";
  return "bg-sky-500/15 text-sky-300 ring-sky-500/20";
};

export const barClass = (status: string): string => {
  if (status === "completed") return "bg-emerald-400";
  if (status === "failed") return "bg-rose-400";
  if (status === "paused") return "bg-amber-400";
  return "bg-sky-400";
};

export const dotClass = (status: string): string => {
  if (status === "completed") return "bg-emerald-400";
  if (status === "failed") return "bg-rose-400";
  if (status === "canceled") return "bg-zinc-500";
  if (status === "paused") return "bg-amber-400";
  return "bg-sky-400";
};

// The daemon's lifecycle, collapsed into the stages a CMS operator cares about.
// Anything not listed (paused, failed, canceled) has no stage of its own.
export const STAGES = [
  { key: "prepare", label: "Prepare", statuses: ["queued", "preparing", "ready"] },
  { key: "transfer", label: "Transfer", statuses: ["connecting", "uploading", "reconnecting"] },
  { key: "assemble", label: "Assemble", statuses: ["assembling"] },
  { key: "verify", label: "Verify", statuses: ["verifying"] },
  { key: "done", label: "Ready", statuses: ["completed"] },
] as const;

export const stageIndexOf = (status: string): number =>
  STAGES.findIndex((s) => (s.statuses as readonly string[]).includes(status));

// Extension-based guess, purely for the row's type chip.
export const kindOf = (filename: string): string => {
  const ext = filename.split(".").pop()?.toLowerCase() ?? "";
  if (["mp4", "mov", "mkv", "m4v", "webm", "avi", "mpg", "mpeg", "ts", "mxf"].includes(ext))
    return "Video";
  if (["mp3", "wav", "aac", "m4a", "flac"].includes(ext)) return "Audio";
  if (["jpg", "jpeg", "png", "webp", "gif", "avif"].includes(ext)) return "Image";
  if (["srt", "vtt", "ttml", "scc"].includes(ext)) return "Subtitle";
  return ext ? ext.toUpperCase() : "File";
};
