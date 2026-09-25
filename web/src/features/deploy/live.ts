import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { getGetDeploymentQueryKey, getListDeploymentsQueryKey, getStreamDeploymentLogsUrl } from '@/lib/api/generated/deployments/deployments';
import { openStream } from '@/lib/sse/stream';
import type { LogLine } from '@/features/pipeline/live';

interface State {
  id?: string;
  chunks: LogLine[];
  ended: boolean;
}

const EMPTY: State = { chunks: [], ended: false };

/**
 * Streams a deployment's log and keeps its query fresh: "status" events refetch the
 * deployment (and the project's history), "log" events append output, "end" closes.
 */
export function useDeploymentStream(deploymentId: string, projectId: string) {
  const queryClient = useQueryClient();
  const [state, setState] = useState<State>(EMPTY);
  useEffect(() => {
    const update = (fn: (s: State) => State) => {
      setState((prev) => fn(prev.id === deploymentId ? prev : { id: deploymentId, chunks: [], ended: false }));
    };
    return openStream(getStreamDeploymentLogsUrl(deploymentId), {
      onMessage: (m) => {
        if (m.event === 'log') {
          const c = JSON.parse(m.data) as LogLine;
          update((s) => ((s.chunks.at(-1)?.seq ?? -1) >= c.seq ? s : { ...s, chunks: [...s.chunks, c] }));
        } else if (m.event === 'status' || m.event === 'end') {
          // A fetch still in flight from before this event may carry the old status, and an
          // invalidation would join it (the query has no data yet): cancel it, then refetch.
          const queryKey = getGetDeploymentQueryKey(deploymentId);
          void queryClient.cancelQueries({ queryKey }).then(() => queryClient.invalidateQueries({ queryKey }));
          void queryClient.invalidateQueries({ queryKey: getListDeploymentsQueryKey(projectId) });
          if (m.event === 'end') update((s) => ({ ...s, ended: true }));
        }
      },
      isFinal: (m) => m.event === 'end',
    });
  }, [deploymentId, projectId, queryClient]);
  return state.id === deploymentId ? state : EMPTY;
}
