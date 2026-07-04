import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
} from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ExternalLink, Maximize2, Share2, ZoomIn, ZoomOut } from "lucide-react";

import {
  getHypothesis,
  getHypothesisGraph,
  type ApiEvidence,
  type GraphEdge,
  type GraphNode,
  type HypothesisGraphData,
} from "@/features/hypothesis/api";
import { type ApiKPI, listKPIs } from "@/features/kpi";
import { DocPreviewSheet, useDocPreview } from "@/features/document";
import { Button } from "@/shared/ui/Button";
import { EmptyState } from "@/shared/ui/EmptyState";
import { ErrorState } from "@/shared/ui/ErrorState";
import { SearchField } from "@/shared/ui/SearchField";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/ui/Select";
import { Skeleton } from "@/shared/ui/Skeleton";
import { currentLocale } from "@/shared/i18n";
import { cn } from "@/shared/lib/cn";

const MAX_DOC_NODES = 160;
const MAX_SEARCH_DOC_NODES = 240;
const OVERVIEW_LINK_LIMIT = 780;
const LABELS_TOP = 14;
const LABELS_ZOOM = 1.45;
const MIN_ZOOM = 0.18;
const MAX_ZOOM = 5.5;

type ViewStance = GraphEdge["class"] | "mixed";

interface ViewNode {
  id: string;
  kind: GraphNode["kind"];
  label: string;
  verdict?: string;
  status?: string;
  kpiId?: string;
  degree: number;
  r: number;
  x: number;
  y: number;
}

interface ViewLink {
  id: string;
  source: string;
  target: string;
  class: ViewStance;
  weight: number;
  chunks: number;
}

interface GraphData {
  nodes: ViewNode[];
  links: ViewLink[];
  droppedDocs: number;
  totalDocs: number;
}

interface GraphTransform {
  x: number;
  y: number;
  k: number;
}

interface CanvasPalette {
  brand: string;
  ok: string;
  risk: string;
  warn: string;
  mixed: string;
  muted: string;
  border: string;
  card: string;
  foreground: string;
}

type Phase = "loading" | "error" | "ready";

function truncate(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}

function clamp(n: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, n));
}

function normalizeQuery(q: string): string {
  return q.trim().toLocaleLowerCase();
}

function chooseStance(counts: Record<GraphEdge["class"], number>): ViewStance {
  if (counts.supports > 0 && counts.contradicts > 0) return "mixed";
  if (counts.contradicts > 0) return "contradicts";
  if (counts.supports > 0) return "supports";
  return "context";
}

function toViewData(
  nodes: GraphNode[],
  edges: GraphEdge[],
  kpiFilter: string,
  stanceFilter: string,
  query: string,
): GraphData {
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const scopedHypIds = new Set(
    nodes
      .filter(
        (node) => node.kind === "hypothesis" && (kpiFilter === "all" || node.kpi_id === kpiFilter),
      )
      .map((node) => node.id),
  );
  const scopedEdges = edges.filter(
    (edge) =>
      scopedHypIds.has(edge.source) &&
      byId.has(edge.target) &&
      (stanceFilter === "all" || edge.class === stanceFilter),
  );
  const linkedDocs = new Set(scopedEdges.map((edge) => edge.target));
  const q = normalizeQuery(query);

  let hypIds = new Set(scopedHypIds);
  let docIds = new Set(linkedDocs);
  if (q) {
    const matchingHypIds = new Set(
      nodes
        .filter(
          (node) =>
            node.kind === "hypothesis" &&
            scopedHypIds.has(node.id) &&
            node.label.toLocaleLowerCase().includes(q),
        )
        .map((node) => node.id),
    );
    const matchingDocIds = new Set(
      nodes
        .filter(
          (node) =>
            node.kind === "document" &&
            linkedDocs.has(node.id) &&
            node.label.toLocaleLowerCase().includes(q),
        )
        .map((node) => node.id),
    );
    const nextHypIds = new Set<string>(matchingHypIds);
    const nextDocIds = new Set<string>(matchingDocIds);
    for (const edge of scopedEdges) {
      if (matchingHypIds.has(edge.source) || matchingDocIds.has(edge.target)) {
        nextHypIds.add(edge.source);
        nextDocIds.add(edge.target);
      }
    }
    hypIds = nextHypIds;
    docIds = nextDocIds;
  }

  const hypDegree = new Map<string, number>();
  const docDegree = new Map<string, number>();
  for (const edge of scopedEdges) {
    if (!hypIds.has(edge.source) || !docIds.has(edge.target)) continue;
    hypDegree.set(edge.source, (hypDegree.get(edge.source) ?? 0) + 1);
    docDegree.set(edge.target, (docDegree.get(edge.target) ?? 0) + 1);
  }

  const docs = nodes.filter((node) => node.kind === "document" && docIds.has(node.id));
  const docLimit = q ? MAX_SEARCH_DOC_NODES : MAX_DOC_NODES;
  const keptDocs =
    docs.length > docLimit
      ? new Set(
          docs
            .toSorted(
              (a, b) =>
                (docDegree.get(b.id) ?? b.degree ?? 0) - (docDegree.get(a.id) ?? a.degree ?? 0) ||
                a.label.localeCompare(b.label),
            )
            .slice(0, docLimit)
            .map((node) => node.id),
        )
      : null;

  const viewNodes = nodes
    .filter((node) =>
      node.kind === "hypothesis"
        ? hypIds.has(node.id)
        : docIds.has(node.id) && (keptDocs?.has(node.id) ?? true),
    )
    .map<ViewNode>((node) => {
      const degree =
        node.kind === "hypothesis" ? (hypDegree.get(node.id) ?? 0) : (docDegree.get(node.id) ?? 0);
      return {
        id: node.id,
        kind: node.kind,
        label: node.label,
        verdict: node.verdict,
        status: node.status,
        kpiId: node.kpi_id,
        degree,
        r:
          node.kind === "hypothesis"
            ? 8 + Math.min(8, Math.sqrt(degree) * 1.2)
            : 3.5 + Math.min(6, Math.sqrt(degree)),
        x: 0,
        y: 0,
      };
    });
  const viewIds = new Set(viewNodes.map((node) => node.id));
  const grouped = new Map<
    string,
    {
      source: string;
      target: string;
      chunks: Set<string>;
      counts: Record<GraphEdge["class"], number>;
    }
  >();
  for (const edge of scopedEdges) {
    if (!viewIds.has(edge.source) || !viewIds.has(edge.target)) continue;
    const key = `${edge.source}|${edge.target}`;
    const item = grouped.get(key) ?? {
      source: edge.source,
      target: edge.target,
      chunks: new Set<string>(),
      counts: { supports: 0, contradicts: 0, context: 0 },
    };
    item.counts[edge.class] += 1;
    item.chunks.add(edge.chunk_id ?? `${key}:${item.chunks.size}`);
    grouped.set(key, item);
  }
  const links = [...grouped.values()]
    .map<ViewLink>((item) => ({
      id: `${item.source}|${item.target}`,
      source: item.source,
      target: item.target,
      class: chooseStance(item.counts),
      weight: item.counts.supports + item.counts.contradicts + item.counts.context,
      chunks: item.chunks.size,
    }))
    .toSorted(
      (a, b) =>
        b.weight - a.weight || a.source.localeCompare(b.source) || a.target.localeCompare(b.target),
    );

  return {
    nodes: viewNodes,
    links,
    droppedDocs: keptDocs ? docs.length - keptDocs.size : 0,
    totalDocs: docs.length,
  };
}

function cssVar(styles: CSSStyleDeclaration, name: string, fallback: string, depth = 0): string {
  if (depth > 4) return fallback;
  const value = styles.getPropertyValue(name).trim();
  if (!value) return fallback;
  const match = /^var\((--[^,\s)]+)/.exec(value);
  if (match?.[1]) return cssVar(styles, match[1], fallback, depth + 1);
  return value;
}

const paletteCache = new WeakMap<HTMLCanvasElement, { key: string; palette: CanvasPalette }>();

function themeKey(): string {
  const explicit = document.documentElement.dataset.theme;
  if (explicit) return explicit;
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function paletteFor(canvas: HTMLCanvasElement): CanvasPalette {
  const key = themeKey();
  const cached = paletteCache.get(canvas);
  if (cached && cached.key === key) return cached.palette;
  const palette = computePalette(canvas);
  paletteCache.set(canvas, { key, palette });
  return palette;
}

function computePalette(canvas: HTMLCanvasElement): CanvasPalette {
  const styles = getComputedStyle(canvas);
  return {
    brand: cssVar(styles, "--brand", "#4f46e5"),
    ok: cssVar(styles, "--ok", "#15803d"),
    risk: cssVar(styles, "--risk", "#dc2626"),
    warn: cssVar(styles, "--warn", "#b45309"),
    mixed: "#7c3aed",
    muted: cssVar(styles, "--muted-foreground", "#64748b"),
    border: cssVar(styles, "--border", "#d4d4d8"),
    card: cssVar(styles, "--card", "#ffffff"),
    foreground: cssVar(styles, "--foreground", "#111827"),
  };
}

function nodeFill(node: ViewNode, palette: CanvasPalette): string {
  if (node.kind === "document") return palette.muted;
  if (node.verdict === "supported") return palette.ok;
  if (node.verdict === "refuted") return palette.risk;
  if (node.verdict === "mixed" || node.verdict === "insufficient") return palette.warn;
  return palette.brand;
}

function linkColor(link: ViewLink, palette: CanvasPalette): string {
  if (link.class === "supports") return palette.ok;
  if (link.class === "contradicts") return palette.risk;
  if (link.class === "mixed") return palette.mixed;
  return palette.border;
}

function verdictTone(verdict: string | undefined): {
  dot: string;
  text: string;
  labelKey: "supported" | "refuted" | "mixed" | "insufficient" | "unverified";
} {
  if (verdict === "supported") return { dot: "var(--ok)", text: "text-ok", labelKey: "supported" };
  if (verdict === "refuted") return { dot: "var(--risk)", text: "text-risk", labelKey: "refuted" };
  if (verdict === "mixed") return { dot: "var(--warn)", text: "text-warn", labelKey: "mixed" };
  if (verdict === "insufficient")
    return { dot: "var(--warn)", text: "text-warn", labelKey: "insufficient" };
  return { dot: "var(--brand)", text: "text-muted-foreground", labelKey: "unverified" };
}

function roundedRect(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  w: number,
  h: number,
  r: number,
) {
  const radius = Math.min(r, w / 2, h / 2);
  ctx.beginPath();
  ctx.moveTo(x + radius, y);
  ctx.arcTo(x + w, y, x + w, y + h, radius);
  ctx.arcTo(x + w, y + h, x, y + h, radius);
  ctx.arcTo(x, y + h, x, y, radius);
  ctx.arcTo(x, y, x + w, y, radius);
  ctx.closePath();
}

function prepareCanvas(canvas: HTMLCanvasElement) {
  const rect = canvas.getBoundingClientRect();
  const dpr = Math.max(1, window.devicePixelRatio || 1);
  const width = Math.max(1, Math.round(rect.width * dpr));
  const height = Math.max(1, Math.round(rect.height * dpr));
  if (canvas.width !== width || canvas.height !== height) {
    canvas.width = width;
    canvas.height = height;
  }
  const ctx = canvas.getContext("2d");
  if (!ctx) return null;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, rect.width, rect.height);
  return { ctx, w: rect.width, h: rect.height };
}

function drawLabel(
  ctx: CanvasRenderingContext2D,
  node: ViewNode,
  text: string,
  palette: CanvasPalette,
  k: number,
  strong: boolean,
) {
  const fontSize = (strong ? 12 : 10.5) / k;
  ctx.font = `${strong ? 650 : 560} ${fontSize}px "Golos Text", Inter, sans-serif`;
  ctx.textBaseline = "middle";
  const label = truncate(text, strong ? 44 : 32);
  const textW = ctx.measureText(label).width;
  const x = node.x + node.r + 7 / k;
  const y = node.y;
  if (strong) {
    roundedRect(ctx, x - 5 / k, y - 10 / k, textW + 10 / k, 20 / k, 6 / k);
    ctx.fillStyle = palette.card;
    ctx.globalAlpha = 0.9;
    ctx.fill();
    ctx.globalAlpha = 1;
  }
  ctx.fillStyle = palette.foreground;
  ctx.fillText(label, x, y);
}

function drawGraph({
  canvas,
  nodes,
  links,
  transform,
  activeId,
  selectedId,
  labelIds,
  neighbors,
}: {
  canvas: HTMLCanvasElement;
  nodes: ViewNode[];
  links: ViewLink[];
  transform: GraphTransform;
  activeId: string | null;
  selectedId: string | null;
  labelIds: Set<string>;
  neighbors: Set<string> | null;
}) {
  const prepared = prepareCanvas(canvas);
  if (!prepared) return;
  const { ctx, w, h } = prepared;
  const palette = paletteFor(canvas);
  const nodeById = new Map(nodes.map((node) => [node.id, node]));

  ctx.save();
  ctx.translate(w / 2 + transform.x, h / 2 + transform.y);
  ctx.scale(transform.k, transform.k);
  ctx.lineCap = "round";

  links.forEach((link, index) => {
    const source = nodeById.get(link.source);
    const target = nodeById.get(link.target);
    if (!source || !target) return;
    const focused = activeId !== null && (link.source === activeId || link.target === activeId);
    const near = neighbors !== null && neighbors.has(link.source) && neighbors.has(link.target);
    if (activeId === null && index > OVERVIEW_LINK_LIMIT) return;
    if (activeId !== null && !near && index > 340) return;

    const dx = target.x - source.x;
    const dy = target.y - source.y;
    const len = Math.max(1, Math.hypot(dx, dy));
    const bendSeed = (source.id.charCodeAt(0) + target.id.charCodeAt(target.id.length - 1)) % 17;
    const bend = ((bendSeed - 8) * 2.2) / transform.k;
    const mx = (source.x + target.x) / 2 - (dy / len) * bend;
    const my = (source.y + target.y) / 2 + (dx / len) * bend;

    ctx.beginPath();
    ctx.moveTo(source.x, source.y);
    ctx.quadraticCurveTo(mx, my, target.x, target.y);
    ctx.strokeStyle = linkColor(link, palette);
    ctx.lineWidth =
      (focused
        ? 2.3 + Math.min(2.2, Math.sqrt(link.weight) * 0.5)
        : 0.75 + Math.min(1.4, Math.sqrt(link.weight) * 0.2)) / transform.k;
    ctx.globalAlpha =
      activeId === null
        ? link.class === "context"
          ? 0.12
          : 0.2
        : focused
          ? 0.9
          : near
            ? 0.22
            : 0.045;
    ctx.stroke();
  });

  const ordered = nodes.toSorted((a, b) =>
    a.kind === b.kind ? a.degree - b.degree : a.kind === "document" ? -1 : 1,
  );
  for (const node of ordered) {
    const focused = activeId === node.id;
    const selected = selectedId === node.id;
    const near = neighbors === null || neighbors.has(node.id);
    const radius = Math.max(
      node.r,
      node.kind === "hypothesis" ? 4.8 / transform.k : 3.2 / transform.k,
    );

    ctx.beginPath();
    ctx.arc(node.x, node.y, radius, 0, Math.PI * 2);
    ctx.fillStyle = nodeFill(node, palette);
    ctx.globalAlpha = near ? 1 : 0.16;
    ctx.fill();
    ctx.strokeStyle = selected || focused ? palette.foreground : palette.card;
    ctx.lineWidth =
      (selected || focused ? 2.5 : node.kind === "hypothesis" ? 1.8 : 1.1) / transform.k;
    ctx.globalAlpha = near ? 1 : 0.2;
    ctx.stroke();
  }

  for (const node of nodes) {
    const isFocus = activeId === node.id || selectedId === node.id;
    const shouldLabel =
      isFocus ||
      labelIds.has(node.id) ||
      (node.kind === "hypothesis" && transform.k >= LABELS_ZOOM && node.degree > 0) ||
      (node.kind === "document" && transform.k >= 2.35 && (neighbors?.has(node.id) ?? false));
    if (!shouldLabel) continue;
    const near = neighbors === null || neighbors.has(node.id);
    if (!near && !isFocus) continue;
    ctx.globalAlpha = near || isFocus ? 1 : 0.22;
    drawLabel(ctx, node, node.label, palette, transform.k, isFocus);
  }

  ctx.restore();
  ctx.globalAlpha = 1;
}

function buildNeighbors(id: string | null, links: ViewLink[]): Set<string> | null {
  if (id === null) return null;
  const set = new Set([id]);
  for (const link of links) {
    if (link.source === id) set.add(link.target);
    if (link.target === id) set.add(link.source);
  }
  return set;
}

function StatChip({ label, value }: { label: string; value: string }) {
  return (
    <span className="inline-flex items-baseline gap-1.5">
      <span className="font-mono text-sm font-semibold tabular-nums text-foreground">{value}</span>
      <span className="text-xs text-muted-foreground">{label}</span>
    </span>
  );
}

export function HypothesisGraphView() {
  const { t } = useTranslation("hypothesis");
  const navigate = useNavigate();
  const { preview, openCitation, close: closePreview, retry: retryPreview } = useDocPreview();

  const [phase, setPhase] = useState<Phase>("loading");
  const [raw, setRaw] = useState<HypothesisGraphData>({ nodes: [], edges: [] });
  const [kpis, setKpis] = useState<ApiKPI[]>([]);
  const [kpiFilter, setKpiFilter] = useState("all");
  const [stanceFilter, setStanceFilter] = useState("all");
  const [query, setQuery] = useState("");
  const [positions, setPositions] = useState(new Map<string, { x: number; y: number }>());
  const [viewport, setViewport] = useState({ w: 0, h: 0 });
  const [transform, setTransform] = useState<GraphTransform>({ x: 0, y: 0, k: 1 });
  const [hoveredId, setHoveredId] = useState<string | null>(null);
  const [hoverPoint, setHoverPoint] = useState<{ x: number; y: number } | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [evidence, setEvidence] = useState<ApiEvidence[]>([]);

  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const dragRef = useRef<{
    x: number;
    y: number;
    nodeId: string | null;
    transform: GraphTransform;
    moved: boolean;
  } | null>(null);
  const interacted = useRef(false);
  const requestRef = useRef(0);
  const lastHoverRef = useRef<string | null>(null);
  const fmt = useMemo(() => new Intl.NumberFormat(currentLocale()), []);

  const load = useCallback(async () => {
    setPhase("loading");
    try {
      const [graph, kpiList] = await Promise.all([getHypothesisGraph(), listKPIs()]);
      setRaw(graph);
      setKpis(kpiList);
      setPhase("ready");
    } catch {
      setPhase("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const data = useMemo(
    () => toViewData(raw.nodes, raw.edges, kpiFilter, stanceFilter, query),
    [raw, kpiFilter, stanceFilter, query],
  );

  const nodes = useMemo(
    () =>
      data.nodes.map((node) => {
        const pos = positions.get(node.id);
        return pos ? { ...node, x: pos.x, y: pos.y } : node;
      }),
    [data.nodes, positions],
  );
  const nodeById = useMemo(() => new Map(nodes.map((node) => [node.id, node])), [nodes]);
  const topHypotheses = useMemo(
    () =>
      nodes
        .filter((node) => node.kind === "hypothesis" && node.degree > 0)
        .toSorted((a, b) => b.degree - a.degree || a.label.localeCompare(b.label))
        .slice(0, 8),
    [nodes],
  );
  const indexHypotheses = useMemo(
    () =>
      nodes
        .filter((node) => node.kind === "hypothesis")
        .toSorted((a, b) => b.degree - a.degree || a.label.localeCompare(b.label)),
    [nodes],
  );
  const labelIds = useMemo(
    () => new Set(topHypotheses.slice(0, LABELS_TOP).map((node) => node.id)),
    [topHypotheses],
  );
  const activeId = hoveredId ?? selectedId;
  const neighbors = useMemo(() => buildNeighbors(activeId, data.links), [activeId, data.links]);
  const focusId = selectedId ?? topHypotheses[0]?.id ?? null;
  const focusNode = focusId ? (nodeById.get(focusId) ?? null) : null;
  const docsByHyp = useMemo(() => {
    const map = new Map<string, number>();
    for (const link of data.links) map.set(link.source, (map.get(link.source) ?? 0) + 1);
    return map;
  }, [data.links]);
  const focusKind = focusNode?.kind;
  useEffect(() => {
    if (!focusId || focusKind !== "hypothesis") {
      setEvidence([]);
      return;
    }
    let alive = true;
    getHypothesis(focusId)
      .then((h) => alive && setEvidence(h.evidence ?? []))
      .catch(() => alive && setEvidence([]));
    return () => {
      alive = false;
    };
  }, [focusId, focusKind]);
  const evidenceByNode = useMemo(() => {
    const map = new Map<string, ApiEvidence[]>();
    for (const ev of evidence) {
      const key = ev.document_id ?? `file:${ev.filename}`;
      const list = map.get(key);
      if (list) list.push(ev);
      else map.set(key, [ev]);
    }
    return map;
  }, [evidence]);

  const fitToGraph = useCallback(
    (force = false) => {
      if (
        (!force && interacted.current) ||
        nodes.length === 0 ||
        viewport.w === 0 ||
        viewport.h === 0
      )
        return;
      let minX = Infinity;
      let minY = Infinity;
      let maxX = -Infinity;
      let maxY = -Infinity;
      for (const node of nodes) {
        minX = Math.min(minX, node.x - node.r - 80);
        minY = Math.min(minY, node.y - node.r - 36);
        maxX = Math.max(maxX, node.x + node.r + (node.kind === "hypothesis" ? 230 : 80));
        maxY = Math.max(maxY, node.y + node.r + 36);
      }
      const pad = viewport.w < 760 ? 24 : 42;
      const k = clamp(
        Math.min(
          (viewport.w - pad * 2) / Math.max(1, maxX - minX),
          (viewport.h - pad * 2) / Math.max(1, maxY - minY),
        ),
        MIN_ZOOM,
        1.85,
      );
      setTransform({
        k,
        x: -((minX + maxX) / 2) * k,
        y: -((minY + maxY) / 2) * k,
      });
    },
    [nodes, viewport],
  );

  useEffect(() => {
    if (phase !== "ready") return;
    const canvas = canvasRef.current;
    if (!canvas) return;
    const resize = () => {
      const rect = canvas.getBoundingClientRect();
      setViewport({ w: rect.width, h: rect.height });
    };
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(canvas);
    return () => observer.disconnect();
  }, [phase]);

  useEffect(() => {
    if (phase !== "ready" || data.nodes.length === 0) {
      setPositions(new Map());
      return;
    }
    setPositions(new Map());
    const requestId = requestRef.current + 1;
    requestRef.current = requestId;
    const worker = new Worker(new URL("./graphLayout.worker.ts", import.meta.url), {
      type: "module",
    });
    worker.addEventListener(
      "message",
      (
        event: MessageEvent<{ requestId: number; nodes: { id: string; x: number; y: number }[] }>,
      ) => {
        if (event.data.requestId !== requestId) return;
        interacted.current = false;
        setPositions(new Map(event.data.nodes.map((node) => [node.id, { x: node.x, y: node.y }])));
      },
    );
    worker.postMessage(
      {
        requestId,
        nodes: data.nodes.map((node) => ({
          id: node.id,
          kind: node.kind,
          degree: node.degree,
          r: node.r,
        })),
        links: data.links.map((link) => ({
          source: link.source,
          target: link.target,
          weight: link.weight,
        })),
      },
      [],
    );
    return () => worker.terminate();
  }, [data.links, data.nodes, phase]);

  useEffect(() => {
    if (positions.size > 0) fitToGraph(false);
  }, [fitToGraph, positions.size]);

  useEffect(() => {
    if (!selectedId && positions.size > 0 && topHypotheses[0]) {
      setSelectedId(topHypotheses[0].id);
    }
  }, [positions.size, selectedId, topHypotheses]);

  useEffect(() => {
    if (selectedId && !nodeById.has(selectedId)) setSelectedId(null);
  }, [nodeById, selectedId]);

  useEffect(() => {
    if (phase !== "ready") return;
    const canvas = canvasRef.current;
    if (!canvas) return;
    drawGraph({
      canvas,
      nodes,
      links: data.links,
      transform,
      activeId,
      selectedId,
      labelIds,
      neighbors,
    });
  }, [activeId, data.links, labelIds, neighbors, nodes, phase, selectedId, transform, viewport]);

  const hitTest = useCallback(
    (x: number, y: number) => {
      const rect = canvasRef.current?.getBoundingClientRect();
      const w = rect?.width ?? viewport.w;
      const h = rect?.height ?? viewport.h;
      let best: ViewNode | null = null;
      let bestDist = Infinity;
      for (const node of nodes) {
        const sx = w / 2 + transform.x + node.x * transform.k;
        const sy = h / 2 + transform.y + node.y * transform.k;
        const radius = Math.max(node.r * transform.k, node.kind === "hypothesis" ? 18 : 12);
        const dist = Math.hypot(x - sx, y - sy);
        if (dist <= radius && dist < bestDist) {
          best = node;
          bestDist = dist;
        }
      }
      return best;
    },
    [nodes, transform, viewport],
  );

  const localPoint = useCallback((e: ReactPointerEvent<HTMLCanvasElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    return { x: e.clientX - rect.left, y: e.clientY - rect.top };
  }, []);

  const centerNode = useCallback(
    (id: string) => {
      const node = nodeById.get(id);
      if (!node) return;
      interacted.current = true;
      setSelectedId(id);
      setTransform((prev) => {
        const k = Math.max(prev.k, 1.22);
        return { k, x: -node.x * k, y: -node.y * k };
      });
    },
    [nodeById],
  );

  const openNode = useCallback(
    (id: string) => {
      const node = nodeById.get(id);
      if (!node) return;
      if (node.kind === "hypothesis") {
        navigate(`/hypotheses/${node.id}`);
      } else if (!node.id.startsWith("file:")) {
        openCitation({ documentId: node.id, filename: node.label });
      }
    },
    [navigate, nodeById, openCitation],
  );

  const openRelated = useCallback(
    (id: string) => {
      const node = nodeById.get(id);
      if (!node) return;
      if (node.kind === "hypothesis") {
        navigate(`/hypotheses/${node.id}`);
        return;
      }
      const ev = (evidenceByNode.get(node.id) ?? [])[0];
      if (!ev && node.id.startsWith("file:")) return;
      openCitation({
        documentId: ev?.document_id ?? node.id,
        chunkId: ev?.chunk_id,
        filename: node.label,
        snippet: ev?.snippet,
        page: ev?.page_start,
      });
    },
    [evidenceByNode, navigate, nodeById, openCitation],
  );

  const zoomAtCanvasPoint = useCallback(
    (sx: number, sy: number, factor: number, w: number, h: number) => {
      interacted.current = true;
      setTransform((prev) => {
        const k = clamp(prev.k * factor, MIN_ZOOM, MAX_ZOOM);
        const wx = (sx - w / 2 - prev.x) / prev.k;
        const wy = (sy - h / 2 - prev.y) / prev.k;
        return { k, x: sx - w / 2 - wx * k, y: sy - h / 2 - wy * k };
      });
    },
    [],
  );

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const onNativeWheel = (event: WheelEvent) => {
      event.preventDefault();
      const rect = canvas.getBoundingClientRect();
      const sx = event.clientX - rect.left;
      const sy = event.clientY - rect.top;
      const amount = clamp(Math.abs(event.deltaY), 20, 260);
      const factor = event.deltaY < 0 ? 1 + amount / 520 : 1 / (1 + amount / 520);
      zoomAtCanvasPoint(sx, sy, factor, rect.width, rect.height);
    };
    canvas.addEventListener("wheel", onNativeWheel, { passive: false });
    return () => canvas.removeEventListener("wheel", onNativeWheel);
  }, [phase, zoomAtCanvasPoint]);

  const onPointerDown = useCallback(
    (e: ReactPointerEvent<HTMLCanvasElement>) => {
      const p = localPoint(e);
      const node = hitTest(p.x, p.y);
      e.currentTarget.setPointerCapture(e.pointerId);
      dragRef.current = { x: p.x, y: p.y, nodeId: node?.id ?? null, transform, moved: false };
      lastHoverRef.current = node?.id ?? null;
      if (node) {
        setHoveredId(node.id);
        setHoverPoint(p);
      } else {
        interacted.current = true;
        setHoveredId(null);
        setHoverPoint(null);
      }
    },
    [hitTest, localPoint, transform],
  );

  const onPointerMove = useCallback(
    (e: ReactPointerEvent<HTMLCanvasElement>) => {
      const p = localPoint(e);
      const drag = dragRef.current;
      if (drag && drag.nodeId === null) {
        const dx = p.x - drag.x;
        const dy = p.y - drag.y;
        if (Math.hypot(dx, dy) > 2) drag.moved = true;
        setTransform({ ...drag.transform, x: drag.transform.x + dx, y: drag.transform.y + dy });
        return;
      }
      if (drag && drag.nodeId !== null && Math.hypot(p.x - drag.x, p.y - drag.y) > 5) {
        drag.moved = true;
      }
      const node = hitTest(p.x, p.y);
      const id = node?.id ?? null;
      if (id !== lastHoverRef.current) {
        lastHoverRef.current = id;
        setHoveredId(id);
        setHoverPoint(node ? p : null);
      }
    },
    [hitTest, localPoint],
  );

  const onPointerUp = useCallback(
    (e: ReactPointerEvent<HTMLCanvasElement>) => {
      const drag = dragRef.current;
      dragRef.current = null;
      if (!drag?.nodeId || drag.moved) return;
      if (nodeById.get(drag.nodeId)?.kind === "document") {
        openRelated(drag.nodeId);
        return;
      }
      if (selectedId === drag.nodeId) {
        openNode(drag.nodeId);
        return;
      }
      setSelectedId(drag.nodeId);
      lastHoverRef.current = drag.nodeId;
      setHoveredId(drag.nodeId);
      setHoverPoint(localPoint(e));
    },
    [localPoint, nodeById, openNode, openRelated, selectedId],
  );

  const onPointerLeave = useCallback(() => {
    if (dragRef.current) return;
    lastHoverRef.current = null;
    setHoveredId(null);
    setHoverPoint(null);
  }, []);

  const zoomBy = useCallback(
    (factor: number) => {
      const rect = canvasRef.current?.getBoundingClientRect();
      if (!rect) {
        setTransform((prev) => ({ ...prev, k: clamp(prev.k * factor, MIN_ZOOM, MAX_ZOOM) }));
        return;
      }
      zoomAtCanvasPoint(rect.width / 2, rect.height / 2, factor, rect.width, rect.height);
    },
    [zoomAtCanvasPoint],
  );

  if (phase === "loading") return <Skeleton className="mt-6 h-[680px] w-full rounded-xl" />;
  if (phase === "error") {
    return <ErrorState message={t("graph.loadError")} onRetry={() => void load()} />;
  }
  if (!raw.nodes.some((node) => node.kind === "hypothesis")) {
    return (
      <EmptyState icon={Share2} title={t("graph.emptyTitle")} description={t("graph.emptyText")} />
    );
  }

  const hypCount = data.nodes.filter((node) => node.kind === "hypothesis").length;
  const docCount = data.nodes.length - hypCount;
  const tooltipStyle = hoverPoint
    ? ({
        left: Math.min(Math.max(12, hoverPoint.x + 14), Math.max(12, viewport.w - 260)),
        top: Math.min(Math.max(12, hoverPoint.y + 14), Math.max(12, viewport.h - 96)),
      } satisfies CSSProperties)
    : undefined;

  return (
    <div className="mt-4 flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-5 gap-y-1.5 px-0.5">
        <StatChip value={fmt.format(hypCount)} label={t("graph.stats.hypotheses")} />
        <StatChip value={fmt.format(docCount)} label={t("graph.stats.documents")} />
        <StatChip value={fmt.format(data.links.length)} label={t("graph.stats.links")} />
        {data.droppedDocs > 0 && (
          <span className="text-xs text-muted-foreground">
            {t("graph.stats.hiddenDocs", { count: data.droppedDocs })}
          </span>
        )}
      </div>

      <div className="flex flex-col gap-3 rounded-xl border bg-card p-3 lg:flex-row lg:items-center lg:justify-between">
        <div className="flex flex-1 flex-col gap-3 md:flex-row md:items-center">
          <SearchField
            value={query}
            onChange={setQuery}
            placeholder={t("graph.searchPlaceholder")}
            ariaLabel={t("graph.searchAria")}
            className="w-full md:max-w-sm"
          />
          {kpis.length > 0 && (
            <Select value={kpiFilter} onValueChange={setKpiFilter}>
              <SelectTrigger size="sm" className="w-full md:w-auto md:min-w-52">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t("board.allGoals")}</SelectItem>
                {kpis.map((kpi) => (
                  <SelectItem key={kpi.id} value={kpi.id}>
                    {kpi.title}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          <Select value={stanceFilter} onValueChange={setStanceFilter}>
            <SelectTrigger size="sm" className="w-full md:w-auto md:min-w-40">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("graph.stanceAll")}</SelectItem>
              <SelectItem value="supports">{t("graph.stances.supports")}</SelectItem>
              <SelectItem value="contradicts">{t("graph.stances.contradicts")}</SelectItem>
              <SelectItem value="context">{t("graph.stances.context")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant="outline"
            size="icon-sm"
            onClick={() => zoomBy(1 / 1.18)}
            title={t("graph.zoomOut")}
            aria-label={t("graph.zoomOut")}
          >
            <ZoomOut className="size-4" aria-hidden />
          </Button>
          <Button
            type="button"
            variant="outline"
            size="icon-sm"
            onClick={() => {
              interacted.current = false;
              fitToGraph(true);
            }}
            title={t("graph.fit")}
            aria-label={t("graph.fit")}
          >
            <Maximize2 className="size-4" aria-hidden />
          </Button>
          <Button
            type="button"
            variant="outline"
            size="icon-sm"
            onClick={() => zoomBy(1.18)}
            title={t("graph.zoomIn")}
            aria-label={t("graph.zoomIn")}
          >
            <ZoomIn className="size-4" aria-hidden />
          </Button>
        </div>
      </div>

      <div className="grid gap-4 xl:h-[calc(100vh-19rem)] xl:min-h-[560px] xl:grid-cols-[minmax(320px,420px)_minmax(0,1fr)]">
        <aside className="flex max-h-[46vh] flex-col overflow-hidden rounded-xl border bg-card xl:max-h-none">
          <div className="border-b px-4 py-3">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-semibold">{t("graph.quickNav")}</h3>
              <span className="rounded-md bg-secondary px-1.5 py-0.5 font-mono text-xs tabular-nums text-muted-foreground">
                {fmt.format(indexHypotheses.length)}
              </span>
            </div>
            <p className="mt-1 text-xs leading-snug text-muted-foreground">
              {t("graph.quickNavHint")}
            </p>
          </div>
          {indexHypotheses.length === 0 ? (
            <p className="px-4 py-3 text-xs text-muted-foreground">{t("graph.noRelated")}</p>
          ) : (
            <div className="flex-1 space-y-0.5 overflow-y-auto p-2">
              {indexHypotheses.map((node) => {
                const tone = verdictTone(node.verdict);
                const active = selectedId === node.id;
                const linked = docsByHyp.get(node.id) ?? 0;
                return (
                  <div
                    key={node.id}
                    className={cn(
                      "group flex items-start rounded-lg pr-1 transition-colors hover:bg-secondary",
                      active && "bg-brand-wash hover:bg-brand-wash",
                    )}
                  >
                    <button
                      type="button"
                      aria-label={node.label}
                      aria-current={active}
                      className="flex min-w-0 flex-1 items-start gap-2.5 rounded-lg px-2.5 py-2 text-left"
                      onClick={() => centerNode(node.id)}
                    >
                      <span
                        className="mt-[3px] size-2.5 shrink-0 rounded-full ring-2 ring-card"
                        style={{ background: tone.dot }}
                        aria-hidden
                      />
                      <span className="min-w-0 flex-1">
                        <span className="line-clamp-3 text-[13px] font-medium leading-snug">
                          {node.label}
                        </span>
                        <span className="mt-0.5 block text-[11px] leading-tight">
                          <span className={tone.text}>{t(`verdicts.${tone.labelKey}`)}</span>
                          <span className="text-muted-foreground">
                            {" · "}
                            {linked > 0
                              ? t("graph.sourceCount", { count: linked })
                              : t("graph.noSources")}
                          </span>
                        </span>
                      </span>
                    </button>
                    <button
                      type="button"
                      title={t("graph.openHypothesis")}
                      aria-label={t("graph.openHypothesis")}
                      className={cn(
                        "mt-1.5 shrink-0 rounded-md p-1.5 text-muted-foreground transition-opacity hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100 xl:opacity-0",
                        active && "opacity-100",
                      )}
                      onClick={() => openNode(node.id)}
                    >
                      <ExternalLink className="size-3.5" aria-hidden />
                    </button>
                  </div>
                );
              })}
            </div>
          )}
        </aside>

        <div className="relative h-[64vh] overflow-hidden rounded-xl border bg-card xl:h-auto">
          <canvas
            ref={canvasRef}
            className={cn(
              "block size-full touch-none select-none",
              hoveredId ? "cursor-pointer" : "cursor-grab",
            )}
            aria-label={t("graph.aria")}
            onPointerDown={onPointerDown}
            onPointerMove={onPointerMove}
            onPointerUp={onPointerUp}
            onPointerCancel={onPointerUp}
            onPointerLeave={onPointerLeave}
          />
          {positions.size === 0 && (
            <div className="pointer-events-none absolute inset-0 grid place-items-center bg-card/70">
              <Skeleton className="h-20 w-64 rounded-xl" />
            </div>
          )}
          <div className="pointer-events-none absolute bottom-2 left-2 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border bg-card/85 px-2.5 py-1.5 text-[11px] text-muted-foreground backdrop-blur">
            <span className="inline-flex items-center gap-1.5">
              <span className="size-2 rounded-full" style={{ background: "var(--ok)" }} />
              {t("graph.nodeLegend.supported")}
            </span>
            <span className="inline-flex items-center gap-1.5">
              <span className="size-2 rounded-full" style={{ background: "var(--risk)" }} />
              {t("graph.nodeLegend.refuted")}
            </span>
            <span className="inline-flex items-center gap-1.5">
              <span className="size-2 rounded-full" style={{ background: "var(--warn)" }} />
              {t("graph.nodeLegend.mixed")}
            </span>
            <span className="inline-flex items-center gap-1.5">
              <span className="size-2 rounded-full" style={{ background: "var(--brand)" }} />
              {t("graph.nodeLegend.unverified")}
            </span>
            <span className="inline-flex items-center gap-1.5">
              <span
                className="size-2 rounded-full"
                style={{ background: "var(--muted-foreground)" }}
              />
              {t("graph.nodeLegend.document")}
            </span>
          </div>
          {hoveredId && hoverPoint && (
            <div
              className="pointer-events-none absolute max-w-64 rounded-lg border bg-popover px-3 py-2 text-xs shadow-lg"
              style={tooltipStyle}
            >
              <div className="mb-1 flex items-center gap-2">
                <span
                  className="size-2 rounded-full"
                  style={{
                    background:
                      nodeById.get(hoveredId)?.kind === "document"
                        ? "var(--muted-foreground)"
                        : "var(--brand)",
                  }}
                />
                <span className="font-medium text-foreground">
                  {nodeById.get(hoveredId)?.kind === "document"
                    ? t("graph.document")
                    : t("graph.hypothesis")}
                </span>
              </div>
              <p className="line-clamp-2 text-muted-foreground">{nodeById.get(hoveredId)?.label}</p>
            </div>
          )}
        </div>
      </div>

      <DocPreviewSheet
        doc={preview ? (preview.full ?? preview.doc) : null}
        fragmentId={preview?.fragmentId}
        loading={preview ? !preview.full && !preview.failed : false}
        error={preview?.failed ?? false}
        onRetry={retryPreview}
        onClose={closePreview}
      />
    </div>
  );
}
