import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Sparkles } from "lucide-react";
import { toast } from "sonner";
import {
  HypothesisCard,
  generateHypotheses,
  isResearchDirection,
  listHypotheses,
  type ApiHypothesis,
} from "@/features/hypothesis";
import { Button } from "@/shared/ui/Button";
import { Kicker } from "@/shared/ui/Kicker";

const LIST_LIMIT = 6;
const GENERATE_COUNT = 3;

/** Гипотезы, чьи доказательства ссылаются на источники ответа, + запуск
 *  генерации новых строго по этим документам (асинхронный job). */
export function SourceHypotheses({
  documentIds,
  question,
}: {
  documentIds: string[];
  question: string;
}) {
  const { t } = useTranslation("search");
  const navigate = useNavigate();
  const [items, setItems] = useState<ApiHypothesis[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [generating, setGenerating] = useState(false);
  const idsKey = documentIds.join(",");

  const load = useCallback(async () => {
    try {
      setItems(await listHypotheses({ document_ids: idsKey, limit: LIST_LIMIT }));
      setFailed(false);
    } catch {
      setFailed(true);
    }
  }, [idsKey]);

  useEffect(() => {
    if (idsKey !== "") void load();
  }, [idsKey, load]);

  if (idsKey === "") return null;

  const generate = async () => {
    setGenerating(true);
    try {
      const created = await generateHypotheses({
        kpi_title: question,
        document_ids: idsKey.split(","),
        count: GENERATE_COUNT,
      });
      toast.success(t("sourceHypotheses.generated", { count: created.length }));
      await load();
    } catch {
      toast.error(t("sourceHypotheses.generateError"));
    } finally {
      setGenerating(false);
    }
  };

  return (
    <div className="mt-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Kicker>{t("sourceHypotheses.heading")}</Kicker>
        <Button size="sm" variant="outline" disabled={generating} onClick={() => void generate()}>
          <Sparkles className="size-3.5" aria-hidden />
          {t("sourceHypotheses.generate")}
        </Button>
      </div>
      {generating && (
        <p className="mt-2 text-sm text-muted-foreground" aria-live="polite">
          {t("sourceHypotheses.generating")}
        </p>
      )}
      {failed ? (
        <p className="mt-2 text-sm text-muted-foreground">{t("sourceHypotheses.loadError")}</p>
      ) : items?.length === 0 && !generating ? (
        <p className="mt-2 text-sm text-muted-foreground">{t("sourceHypotheses.empty")}</p>
      ) : items && items.length > 0 ? (
        <div className="mt-2 grid grid-cols-1 gap-4 sm:grid-cols-2">
          {items.map((h) => (
            <HypothesisCard
              key={h.id}
              h={h}
              onOpen={() =>
                navigate(isResearchDirection(h) ? `/directions/${h.id}` : `/hypotheses/${h.id}`)
              }
            />
          ))}
        </div>
      ) : null}
    </div>
  );
}
