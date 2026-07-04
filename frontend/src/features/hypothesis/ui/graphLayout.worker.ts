interface LayoutNode {
  id: string;
  kind: "hypothesis" | "document";
  degree: number;
  r: number;
}

interface LayoutLink {
  source: string;
  target: string;
  weight: number;
}

interface LayoutRequest {
  requestId: number;
  nodes: LayoutNode[];
  links: LayoutLink[];
}

interface PositionedNode {
  id: string;
  x: number;
  y: number;
}

interface LayoutResponse {
  requestId: number;
  nodes: PositionedNode[];
}

type WorkerScope = {
  postMessage: (message: LayoutResponse, transfer: Transferable[]) => void;
  addEventListener: (
    type: "message",
    handler: (event: MessageEvent<LayoutRequest>) => void,
  ) => void;
};

const worker = self as unknown as WorkerScope;

function hashId(id: string): number {
  let h = 2166136261;
  for (let i = 0; i < id.length; i += 1) {
    h ^= id.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

function spiralPoint(index: number, total: number, baseRadius: number, step: number) {
  const golden = Math.PI * (3 - Math.sqrt(5));
  const t = total <= 1 ? 0 : index / Math.max(1, total - 1);
  const radius = baseRadius + Math.sqrt(index + 1) * step + t * 160;
  const angle = index * golden;
  return { x: Math.cos(angle) * radius, y: Math.sin(angle) * radius };
}

function relaxCollisions(
  nodes: PositionedNode[],
  radiusById: Map<string, number>,
  iterations: number,
) {
  const cellSize = 36;
  for (let step = 0; step < iterations; step += 1) {
    const grid = new Map<string, number[]>();
    nodes.forEach((node, index) => {
      const gx = Math.floor(node.x / cellSize);
      const gy = Math.floor(node.y / cellSize);
      const key = `${gx}:${gy}`;
      const bucket = grid.get(key);
      if (bucket) bucket.push(index);
      else grid.set(key, [index]);
    });

    for (const [key, indexes] of grid) {
      const [rawX, rawY] = key.split(":");
      const gx = Number(rawX);
      const gy = Number(rawY);
      const candidates = [...indexes];
      for (let x = gx - 1; x <= gx + 1; x += 1) {
        for (let y = gy - 1; y <= gy + 1; y += 1) {
          if (x === gx && y === gy) continue;
          candidates.push(...(grid.get(`${x}:${y}`) ?? []));
        }
      }

      for (const i of indexes) {
        const a = nodes[i];
        if (!a) continue;
        for (const j of candidates) {
          if (j <= i) continue;
          const b = nodes[j];
          if (!b) continue;
          const minDist = (radiusById.get(a.id) ?? 5) + (radiusById.get(b.id) ?? 5) + 9;
          let dx = b.x - a.x;
          let dy = b.y - a.y;
          let dist = Math.hypot(dx, dy);
          if (dist >= minDist) continue;
          if (dist < 0.001) {
            const angle = (hashId(`${a.id}:${b.id}`) / 2 ** 32) * Math.PI * 2;
            dx = Math.cos(angle);
            dy = Math.sin(angle);
            dist = 1;
          }
          const push = ((minDist - dist) / dist) * 0.45;
          a.x -= dx * push;
          a.y -= dy * push;
          b.x += dx * push;
          b.y += dy * push;
        }
      }
    }
  }
}

function normalize(nodes: PositionedNode[]) {
  if (nodes.length === 0) return;
  let sx = 0;
  let sy = 0;
  for (const node of nodes) {
    sx += node.x;
    sy += node.y;
  }
  sx /= nodes.length;
  sy /= nodes.length;
  for (const node of nodes) {
    node.x = Math.round((node.x - sx) * 100) / 100;
    node.y = Math.round((node.y - sy) * 100) / 100;
  }
}

worker.addEventListener("message", (event) => {
  const { requestId, nodes, links } = event.data;
  const hypotheses = nodes
    .filter((node) => node.kind === "hypothesis")
    .toSorted((a, b) => b.degree - a.degree || a.id.localeCompare(b.id));
  const documents = nodes
    .filter((node) => node.kind === "document")
    .toSorted((a, b) => b.degree - a.degree || a.id.localeCompare(b.id));

  const byId = new Map(nodes.map((node) => [node.id, node]));
  const radiusById = new Map(nodes.map((node) => [node.id, node.r]));
  const hypPosition = new Map<string, PositionedNode>();
  const docLinks = new Map<string, { hypId: string; weight: number }[]>();
  const hypDocs = new Map<string, string[]>();

  links.forEach((link) => {
    if (!byId.has(link.source) || !byId.has(link.target)) return;
    const list = docLinks.get(link.target) ?? [];
    list.push({ hypId: link.source, weight: Math.max(1, link.weight) });
    docLinks.set(link.target, list);
    const docs = hypDocs.get(link.source) ?? [];
    docs.push(link.target);
    hypDocs.set(link.source, docs);
  });

  hypotheses.forEach((node, index) => {
    const p = spiralPoint(index, hypotheses.length, 28, hypotheses.length > 120 ? 30 : 36);
    hypPosition.set(node.id, { id: node.id, x: p.x, y: p.y });
  });

  const docPosition = new Map<string, PositionedNode>();
  const singleParentRank = new Map<string, number>();
  for (const [hypId, docs] of hypDocs) {
    docs
      .toSorted((a, b) => (byId.get(b)?.degree ?? 0) - (byId.get(a)?.degree ?? 0))
      .forEach((docId, index) => {
        singleParentRank.set(`${hypId}:${docId}`, index);
      });
  }

  documents.forEach((node, index) => {
    const connected = docLinks.get(node.id) ?? [];
    if (connected.length === 0) {
      docPosition.set(node.id, { id: node.id, ...spiralPoint(index, documents.length, 560, 18) });
      return;
    }

    let sx = 0;
    let sy = 0;
    let sw = 0;
    let strongest = connected[0];
    for (const edge of connected) {
      const hyp = hypPosition.get(edge.hypId);
      if (!hyp) continue;
      const weight = Math.max(1, edge.weight);
      sx += hyp.x * weight;
      sy += hyp.y * weight;
      sw += weight;
      if (!strongest || weight > strongest.weight) strongest = edge;
    }
    const cx = sw > 0 ? sx / sw : 0;
    const cy = sw > 0 ? sy / sw : 0;
    const shared = connected.length > 1;
    const baseAngle = (hashId(node.id) / 2 ** 32) * Math.PI * 2;

    if (shared) {
      const radius = 92 + Math.min(70, connected.length * 8) + (hashId(node.id) % 30);
      docPosition.set(node.id, {
        id: node.id,
        x: cx + Math.cos(baseAngle) * radius,
        y: cy + Math.sin(baseAngle) * radius,
      });
      return;
    }

    const parent = strongest ? hypPosition.get(strongest.hypId) : null;
    const rank = strongest
      ? (singleParentRank.get(`${strongest.hypId}:${node.id}`) ?? index)
      : index;
    const angle = baseAngle + rank * 0.58;
    const radius = 118 + (rank % 7) * 18 + Math.floor(rank / 7) * 26;
    docPosition.set(node.id, {
      id: node.id,
      x: (parent?.x ?? cx) + Math.cos(angle) * radius,
      y: (parent?.y ?? cy) + Math.sin(angle) * radius,
    });
  });

  const positioned = [...hypPosition.values(), ...docPosition.values()];
  relaxCollisions(positioned, radiusById, nodes.length > 500 ? 6 : 8);
  normalize(positioned);
  worker.postMessage({ requestId, nodes: positioned }, []);
});
