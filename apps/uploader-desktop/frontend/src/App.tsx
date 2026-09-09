import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Dialogs, Events } from "@wailsio/runtime";
import { UploadService } from "../bindings/code.sli.ke/go/vega/apps/uploader-desktop";
import { isTerminal, running, type LogLine, type Stats, type Upload } from "./format";
import { Sidebar, TopBar, type View } from "./layout";
import { ActivityView, IngestView, LibraryView, QueueView, SettingsView } from "./views";
import type { RowActions } from "./ui";

function App() {
  const [uploads, setUploads] = useState<Upload[]>([]);
  const [metrics, setMetrics] = useState<Record<string, number | undefined>>({});
  const [connected, setConnected] = useState(false);
  const [view, setView] = useState<View>("ingest");
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("all");
  const [log, setLog] = useState<LogLine[]>([]);
  // Only the first successful List decides whether we land on Ingest or Queue.
  const routed = useRef(false);

  const refresh = useCallback(async () => {
    try {
      const list = ((await UploadService.List()) ?? []) as Upload[];
      setUploads(list);
      setConnected(true);
      if (!routed.current) {
        routed.current = true;
        if (list.length > 0) setView("queue");
      }
    } catch {
      setConnected(false);
    }
    try {
      setMetrics((await UploadService.Metrics()) ?? {});
    } catch {
      /* metrics are decoration; a dead daemon already shows as disconnected */
    }
  }, []);

  useEffect(() => {
    refresh();
    // Live: the daemon pushes an event on every progress tick and lifecycle
    // change; we simply re-list. A slow poll covers any missed events.
    const off = Events.On("upload.event", (ev) => {
      refresh();
      const e = ev.data as { Kind?: string; UploadID?: string; Fields?: Record<string, unknown> };
      // Progress ticks are far too chatty for a human-readable log.
      if (!e?.Kind || e.Kind === "progress") return;
      const fields = e.Fields ?? {};
      const detail = Object.entries(fields)
        .filter(([k]) => k !== "bytesDone" && k !== "size")
        .map(([k, v]) => `${k}=${String(v)}`)
        .join(" ");
      setLog((prev) =>
        [{ at: new Date(), kind: e.Kind!, uploadID: e.UploadID ?? "", detail }, ...prev].slice(0, 200)
      );
    });
    const timer = setInterval(refresh, 1500);
    return () => {
      off();
      clearInterval(timer);
    };
  }, [refresh]);

  const addFiles = useCallback(async () => {
    const picked = await Dialogs.OpenFile({
      Title: "Choose media to ingest",
      CanChooseFiles: true,
      AllowsMultipleSelection: true,
    });
    for (const path of picked) await UploadService.Enqueue(path);
    if (picked.length > 0) setView("queue");
    refresh();
  }, [refresh]);

  const addFolder = useCallback(async () => {
    const picked = await Dialogs.OpenFile({
      Title: "Choose a folder to ingest",
      CanChooseDirectories: true,
      CanChooseFiles: false,
      AllowsMultipleSelection: true,
    });
    for (const path of picked) await UploadService.Enqueue(path);
    if (picked.length > 0) setView("queue");
    refresh();
  }, [refresh]);

  const act = useCallback(
    (fn: (id: string) => PromiseLike<void>) => (id: string) => () =>
      Promise.resolve(fn(id)).then(refresh).catch(console.error),
    [refresh]
  );

  const actions: RowActions = useMemo(
    () => ({
      pause: act(UploadService.Pause),
      resume: act(UploadService.Resume),
      cancel: act(UploadService.Cancel),
    }),
    [act]
  );

  const stats: Stats = useMemo(() => {
    const active = uploads.filter((u) => running.has(u.status));
    const done = uploads.filter((u) => u.status === "completed");
    return {
      active,
      done,
      paused: uploads.filter((u) => u.status === "paused"),
      failed: uploads.filter((u) => u.status === "failed"),
      inFlight: uploads.filter((u) => !isTerminal(u.status)),
      throughput: active.reduce((sum, u) => sum + u.curBps, 0),
      bytesDone: uploads.reduce((sum, u) => sum + u.bytesDone, 0),
      bytesTotal: uploads.reduce((sum, u) => sum + u.size, 0),
      libraryBytes: done.reduce((sum, u) => sum + u.size, 0),
    };
  }, [uploads]);

  const pauseAll = () =>
    Promise.all(stats.active.map((u) => UploadService.Pause(u.id))).then(refresh).catch(console.error);
  const resumeAll = () =>
    Promise.all([...stats.paused, ...stats.failed].map((u) => UploadService.Resume(u.id)))
      .then(refresh)
      .catch(console.error);

  return (
    <div className="flex h-full text-zinc-100">
      <Sidebar
        view={view}
        setView={setView}
        connected={connected}
        counts={{
          queue: stats.inFlight.length,
          library: stats.done.length,
          activity: log.length,
        }}
        throughput={stats.throughput}
      />

      <div className="flex min-w-0 flex-1 flex-col">
        <TopBar
          view={view}
          query={query}
          setQuery={setQuery}
          onAdd={addFiles}
          uploads={uploads.length}
        />

        <main className="flex-1 overflow-y-auto px-7 pb-8">
          {view === "ingest" && (
            <IngestView
              stats={stats}
              uploads={uploads}
              onAdd={addFiles}
              onAddFolder={addFolder}
              onOpenQueue={() => setView("queue")}
            />
          )}

          {view === "queue" && (
            <QueueView
              uploads={uploads}
              query={query}
              filter={filter}
              setFilter={setFilter}
              actions={actions}
              stats={stats}
              onPauseAll={pauseAll}
              onResumeAll={resumeAll}
              onGoIngest={() => setView("ingest")}
            />
          )}

          {view === "library" && <LibraryView uploads={stats.done} query={query} bytes={stats.libraryBytes} />}

          {view === "activity" && <ActivityView log={log} />}

          {view === "settings" && (
            <SettingsView connected={connected} metrics={metrics} stats={stats} />
          )}
        </main>
      </div>
    </div>
  );
}

export default App;
