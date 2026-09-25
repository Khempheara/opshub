import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { getGetJobQueryKey, getGetRunQueryKey, getListRunsQueryKey, getRunEventsUrl, getStreamJobLogsUrl } from '@/lib/api/generated/pipelines/pipelines';
import { openStream } from '@/lib/sse/stream';

/**
 * Keeps a run (and its project's run list) fresh while it's on screen: the server signals
 * changes over SSE and TanStack Query refetches. Returns whether the stream is connected.
 */
export function useLiveRun(runId: string, projectId: string, selectedJobId?: string) {
  const queryClient = useQueryClient();
  const [live, setLive] = useState(false);
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const close = openStream(getRunEventsUrl(runId), {
      onMessage: (m) => {
        if (m.event !== 'update') return;
        setLive(true);
        // Coalesce bursts (a job finishing and the next starting) into one refetch.
        clearTimeout(timer);
        timer = setTimeout(() => {
          void queryClient.invalidateQueries({ queryKey: getGetRunQueryKey(runId) });
          void queryClient.invalidateQueries({ queryKey: getListRunsQueryKey(projectId) });
          if (selectedJobId) void queryClient.invalidateQueries({ queryKey: getGetJobQueryKey(selectedJobId) });
        }, 150);
      },
      onError: () => {
        setLive(false);
      },
    });
    return () => {
      clearTimeout(timer);
      close();
      setLive(false);
    };
  }, [runId, projectId, selectedJobId, queryClient]);
  return live;
}

export interface LogLine {
  seq: number;
  content: string;
}

interface LogState {
  jobId?: string;
  chunks: LogLine[];
  ended: boolean;
}

const EMPTY: LogState = { chunks: [], ended: false };

/** Streams a job's log: backlog first, then live output until the job ends. */
export function useLogStream(jobId: string | undefined) {
  const [state, setState] = useState<LogState>(EMPTY);
  useEffect(() => {
    if (!jobId) return;
    // State is keyed by job, so switching jobs never shows the previous job's output.
    const update = (fn: (s: LogState) => LogState) => {
      setState((prev) => fn(prev.jobId === jobId ? prev : { jobId, chunks: [], ended: false }));
    };
    return openStream(getStreamJobLogsUrl(jobId), {
      onMessage: (m) => {
        if (m.event === 'log') {
          const c = JSON.parse(m.data) as LogLine;
          // Reconnects resume after the last id, but guard against replays anyway.
          update((s) => ((s.chunks.at(-1)?.seq ?? -1) >= c.seq ? s : { ...s, chunks: [...s.chunks, c] }));
        } else if (m.event === 'end') {
          update((s) => ({ ...s, ended: true }));
        }
      },
      isFinal: (m) => m.event === 'end',
    });
  }, [jobId]);
  return state.jobId === jobId ? state : EMPTY;
}
