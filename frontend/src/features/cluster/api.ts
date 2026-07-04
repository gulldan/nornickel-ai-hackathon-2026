// Theme/cluster endpoints. A cluster carries its corpus-derived technology
// index (ITC) under params.itc — the same shape hypotheses use.
import type { Itc } from "@/features/hypothesis";
import { request } from "@/shared/api/client";

interface ClusterRep {
  document_id?: string;
  filename?: string;
  snippet?: string;
}

export interface ApiCluster {
  id: string;
  owner_id: string;
  label: string;
  summary: string;
  keywords: string[];
  method: string;
  chunk_count: number;
  document_count: number;
  representatives: ClusterRep[];
  /** JSONB bag; carries the theme's ITC under params.itc. */
  params?: { itc?: Itc } & Record<string, unknown>;
  status: string;
  created_at: string;
  updated_at: string;
}

// The same theme list (~600KB) is fetched by five pages, so navigation re-paid
// it on every transition; one shared promise with a short TTL amortizes that.
const CLUSTERS_TTL_MS = 120_000;

let clustersCache: { at: number; promise: Promise<ApiCluster[]> } | null = null;

export function listClusters(): Promise<ApiCluster[]> {
  if (clustersCache !== null && Date.now() - clustersCache.at < CLUSTERS_TTL_MS) {
    return clustersCache.promise;
  }
  const entry: NonNullable<typeof clustersCache> = {
    at: Date.now(),
    promise: request<ApiCluster[]>("/clusters").catch((err: unknown) => {
      // Failures are not cached: drop the entry so the next call retries.
      if (clustersCache === entry) clustersCache = null;
      throw err;
    }),
  };
  clustersCache = entry;
  return entry.promise;
}

/** Drops the cached theme list; call after any cluster mutation elsewhere. */
export function invalidateClusters(): void {
  clustersCache = null;
}

export function getCluster(id: string): Promise<ApiCluster> {
  return request<ApiCluster>(`/clusters/${id}`);
}
