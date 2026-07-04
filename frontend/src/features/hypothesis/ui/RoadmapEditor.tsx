import { useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";

import {
  updateHypothesis,
  type ApiHypothesis,
  type HypothesisDetail,
} from "@/features/hypothesis/api";
import { successCriteriaLines } from "@/features/hypothesis/model";
import { loadAuth } from "@/shared/api/client";
import { Button } from "@/shared/ui/Button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/Dialog";
import { Input } from "@/shared/ui/Input";
import { Label } from "@/shared/ui/Label";

type Plan = NonNullable<HypothesisDetail["experiment_plan"]>;

interface SectionForm {
  key: number;
  title: string;
  purpose: string;
  items: string;
}

let sectionKeySeq = 0;

function nextKey(): number {
  sectionKeySeq += 1;
  return sectionKeySeq;
}

function lines(v: string): string[] {
  return v
    .split("\n")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

const TEXTAREA_CLS =
  "flex w-full resize-none rounded-lg border border-input bg-card px-3 py-2 text-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

function toForm(plan: Plan): {
  sections: SectionForm[];
  criteria: string;
  cost: string;
  time: string;
} {
  return {
    sections: (plan.sections ?? []).map((s) => ({
      key: nextKey(),
      title: s.title ?? "",
      purpose: s.purpose ?? "",
      items: (s.items ?? []).join("\n"),
    })),
    criteria: successCriteriaLines(plan.success_criteria).join("\n"),
    cost: plan.estimated_cost ?? "",
    time: (plan.estimated_time ?? "") || (plan.horizon ?? ""),
  };
}

export function RoadmapEditDialog({
  open,
  onOpenChange,
  hypothesis,
  onChanged,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  hypothesis: ApiHypothesis;
  onChanged: (updated: ApiHypothesis) => void;
}) {
  const { t } = useTranslation("hypothesisDetail");
  const plan = hypothesis.detail?.experiment_plan ?? {};
  const [form, setForm] = useState(() => toForm(plan));
  const [busy, setBusy] = useState(false);

  const setSection = (i: number, patch: Partial<SectionForm>) => {
    setForm((f) => ({
      ...f,
      sections: f.sections.map((s, j) => (j === i ? { ...s, ...patch } : s)),
    }));
  };

  const addSection = () => {
    setForm((f) => ({
      ...f,
      sections: [...f.sections, { key: nextKey(), title: "", purpose: "", items: "" }],
    }));
  };

  const removeSection = (i: number) => {
    setForm((f) => ({ ...f, sections: f.sections.filter((_, j) => j !== i) }));
  };

  const save = async () => {
    setBusy(true);
    try {
      const nextPlan: Plan = {
        ...plan,
        sections: form.sections
          .filter((s) => s.title.trim() !== "" || s.purpose.trim() !== "" || s.items.trim() !== "")
          .map((s) => ({
            title: s.title.trim(),
            purpose: s.purpose.trim(),
            items: lines(s.items),
          })),
        success_criteria: lines(form.criteria),
        estimated_cost: form.cost.trim(),
        estimated_time: form.time.trim(),
      };
      const updated = await updateHypothesis(hypothesis.id, {
        ...hypothesis,
        detail: { ...hypothesis.detail, experiment_plan: nextPlan },
        revision: {
          action: "edited",
          summary: t("roadmapEdit.revisionSummary"),
          editor_id: loadAuth()?.user.username ?? "expert",
        },
      });
      onOpenChange(false);
      onChanged(updated);
      toast.success(t("roadmapEdit.saved"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("roadmapEdit.saveFailed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("roadmapEdit.dialogTitle")}</DialogTitle>
          <DialogDescription>{t("roadmapEdit.dialogDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {form.sections.map((s, i) => (
            <div key={s.key} className="space-y-2 rounded-lg border p-3">
              <div className="flex items-center justify-between gap-2">
                <Label htmlFor={`rm-title-${i}`}>{t("roadmapEdit.stage", { n: i + 1 })}</Label>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => removeSection(i)}
                  aria-label={t("roadmapEdit.removeStage")}
                >
                  <Trash2 className="size-3.5" aria-hidden />
                </Button>
              </div>
              <Input
                id={`rm-title-${i}`}
                value={s.title}
                onChange={(e) => setSection(i, { title: e.target.value })}
                placeholder={t("roadmapEdit.stageTitle")}
              />
              <Input
                value={s.purpose}
                onChange={(e) => setSection(i, { purpose: e.target.value })}
                placeholder={t("roadmapEdit.stagePurpose")}
                aria-label={t("roadmapEdit.stagePurpose")}
              />
              <textarea
                value={s.items}
                onChange={(e) => setSection(i, { items: e.target.value })}
                rows={2}
                className={TEXTAREA_CLS}
                placeholder={t("roadmapEdit.stageItems")}
                aria-label={t("roadmapEdit.stageItems")}
              />
            </div>
          ))}
          <Button size="sm" variant="outline" onClick={addSection}>
            <Plus className="size-3.5" aria-hidden />
            {t("roadmapEdit.addStage")}
          </Button>
          <div className="space-y-1.5">
            <Label htmlFor="rm-criteria">{t("roadmapEdit.criteria")}</Label>
            <textarea
              id="rm-criteria"
              value={form.criteria}
              onChange={(e) => setForm((f) => ({ ...f, criteria: e.target.value }))}
              rows={3}
              className={TEXTAREA_CLS}
              placeholder={t("roadmapEdit.criteriaPlaceholder")}
            />
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="rm-cost">{t("roadmapEdit.cost")}</Label>
              <Input
                id="rm-cost"
                value={form.cost}
                onChange={(e) => setForm((f) => ({ ...f, cost: e.target.value }))}
                placeholder="low / medium / high"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="rm-time">{t("roadmapEdit.time")}</Label>
              <Input
                id="rm-time"
                value={form.time}
                onChange={(e) => setForm((f) => ({ ...f, time: e.target.value }))}
                placeholder="low / medium / high"
              />
            </div>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>
            {t("roadmapEdit.cancel")}
          </Button>
          <Button variant="brand" size="sm" disabled={busy} onClick={() => void save()}>
            {t("roadmapEdit.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
