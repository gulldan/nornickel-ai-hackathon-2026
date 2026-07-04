import { Layers, Target } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ApiHypothesis } from "@/features/hypothesis/api";
import { displayHypothesisTitle, priorityScore } from "@/features/hypothesis/model";
import {
  evidenceStats,
  originMeta,
  verdictMeta,
  type BadgeTone,
} from "@/features/hypothesis/board/model";
import { Badge } from "@/shared/ui/Badge";
import { CardButton, CardContent } from "@/shared/ui/Card";
import { SegmentBar } from "@/shared/ui/SegmentBar";
import { EvidenceTally } from "@/shared/ui/EvidenceTally";
import { cn } from "@/shared/lib/cn";

const SEGMENT_TONE: Record<BadgeTone, "ok" | "risk" | "warn" | "brand" | "ink"> = {
  ok: "ok",
  risk: "risk",
  warn: "warn",
  secondary: "brand",
  brand: "brand",
};

/** Карточка доски: рейтинг приборной шкалой, вердикт, происхождение,
 *  строка доказательств и связи с целью/темой — без прежней перегрузки. */
export function HypothesisCard({
  h,
  kpiTitle,
  clusterTitle,
  owner,
  onOpen,
}: {
  h: ApiHypothesis;
  kpiTitle?: string;
  clusterTitle?: string;
  owner?: string | null;
  onOpen: () => void;
}) {
  const { t } = useTranslation("hypothesis");
  const verdict = verdictMeta(h);
  const origin = originMeta(h);
  const evidence = evidenceStats(h);
  const title = displayHypothesisTitle(h);
  const rank = priorityScore(h);
  const tone = SEGMENT_TONE[verdict?.tone ?? "secondary"];
  return (
    <CardButton onClick={onOpen} className="flex flex-col">
      <CardContent className="flex flex-1 flex-col gap-3 p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            {verdict ? (
              <Badge variant={verdict.tone}>{verdict.label}</Badge>
            ) : (
              <Badge variant="secondary">{t("verdicts.unverified")}</Badge>
            )}
            {origin && <Badge variant={origin.tone}>{origin.label}</Badge>}
            {owner && <Badge variant="outline">{t("card.owner", { name: owner })}</Badge>}
          </div>
          {rank !== null && (
            <div className="shrink-0 text-right">
              <span className="font-mono text-xl font-semibold leading-none tabular-nums">
                {rank}
              </span>
              <span className="font-mono text-xs text-muted-foreground">/100</span>
              <SegmentBar
                className="mt-1 justify-end"
                value={Math.round((rank / 100) * 9)}
                tone={tone}
                label={t("card.ratingAria", { rank })}
              />
            </div>
          )}
        </div>

        <div className="min-w-0">
          <h3 className="line-clamp-2 text-[15px] font-medium leading-snug">{title}</h3>
          <p className="mt-1 line-clamp-2 text-sm text-muted-foreground">{h.statement}</p>
        </div>

        <EvidenceTally
          className="mt-auto"
          supports={evidence.supports}
          contradicts={evidence.contradicts}
          mentions={evidence.context}
          verified={verdict !== null}
        />

        {(kpiTitle || clusterTitle) && (
          <div
            className={cn(
              "grid gap-1 border-t pt-2.5 text-xs text-muted-foreground",
              kpiTitle && clusterTitle && "grid-rows-2",
            )}
          >
            {kpiTitle && (
              <span className="flex min-w-0 items-center gap-1.5">
                <Target className="size-3.5 shrink-0" aria-hidden />
                <span className="truncate">{kpiTitle}</span>
              </span>
            )}
            {clusterTitle && (
              <span className="flex min-w-0 items-center gap-1.5">
                <Layers className="size-3.5 shrink-0" aria-hidden />
                <span className="truncate">{clusterTitle}</span>
              </span>
            )}
          </div>
        )}
      </CardContent>
    </CardButton>
  );
}
