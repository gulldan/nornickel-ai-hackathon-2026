import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { CircleCheck, FileText, Paperclip, Wand2, X } from "lucide-react";
import { toast } from "sonner";

import {
  attachKpiDocuments,
  createKPI,
  detachKpiDocument,
  listKpiDocuments,
  parseKpiPrompt,
  updateKPI,
  type ApiKPI,
  type KpiDocument,
} from "@/features/kpi/api";
import { inferDirection } from "@/features/kpi/model";
import { UPLOAD_ACCEPT, useAttachedUploads, type AttachedUpload } from "@/features/document";
import { clampGenerateCount, generateHypotheses, type ApiHypothesis } from "@/features/hypothesis";
import { useBackgroundTasks } from "@/features/task";
import { Badge } from "@/shared/ui/Badge";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/ui/Select";
import { Spinner } from "@/shared/ui/Spinner";
import {
  GENERATION_COUNT_MAX,
  GENERATION_COUNT_MIN,
  loadHypothesisRuntimeSettings,
} from "@/shared/appSettings";

const NEW_GOAL = "__new__";

async function parsedBody(text: string) {
  const parsed = await parseKpiPrompt(text);
  const title = (parsed.title ?? "").trim() || (text.split("\n")[0] ?? "").trim().slice(0, 120);
  return {
    title,
    description: text,
    metric: (parsed.metric ?? "").trim(),
    unit: (parsed.unit ?? "").trim(),
    direction:
      (parsed.direction ?? "").trim() ||
      inferDirection(title, parsed.baseline ?? null, parsed.target ?? null),
    function_area: (parsed.function_area ?? "").trim(),
    baseline: parsed.baseline ?? null,
    target: parsed.target ?? null,
    constraints: (parsed.constraints ?? "").trim(),
  };
}

/** Единая модалка цели: создание, редактирование и генерация в одном месте.
 *  Одно текстовое поле (эксперты просили без выпадающих списков для данных) и
 *  документы входных данных прямо в форме — при редактировании видно, что было
 *  задано и что приложено. */
export function GoalPromptDialog({
  open,
  onOpenChange,
  kpis,
  initialKpiId,
  initialPrompt,
  editKpi,
  onCreated,
  onUpdated,
  onGenerated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  kpis: ApiKPI[];
  initialKpiId?: string;
  initialPrompt?: string;
  editKpi?: ApiKPI | null;
  onCreated?: (kpi: ApiKPI) => void;
  onUpdated?: (kpi: ApiKPI) => void;
  onGenerated?: (created: ApiHypothesis[]) => void;
}) {
  const { t } = useTranslation("kpi");
  const { runTask } = useBackgroundTasks();
  const uploads = useAttachedUploads();
  const [kpiId, setKpiId] = useState<string>(NEW_GOAL);
  const [prompt, setPrompt] = useState("");
  const [attached, setAttached] = useState<KpiDocument[]>([]);
  const [count, setCount] = useState(() => loadHypothesisRuntimeSettings().defaultGenerateCount);
  const [busy, setBusy] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const dragDepth = useRef(0);
  const savedPrompt = useRef("");

  useEffect(() => {
    if (!open) return;
    const target = editKpi ?? null;
    setKpiId(target ? target.id : (initialKpiId ?? NEW_GOAL));
    savedPrompt.current = target ? target.description : "";
    setPrompt(target ? target.description : (initialPrompt ?? ""));
    setCount(loadHypothesisRuntimeSettings().defaultGenerateCount);
    setAttached([]);
    const docsFor = target?.id ?? initialKpiId;
    if (docsFor) {
      listKpiDocuments(docsFor)
        .then(setAttached)
        .catch(() => setAttached([]));
    }
  }, [open, initialKpiId, initialPrompt, editKpi]);

  const activeKpis = kpis.filter((k) => k.status !== "archived");
  const existing =
    editKpi ?? (kpiId === NEW_GOAL ? null : (kpis.find((k) => k.id === kpiId) ?? null));
  const promptReady = existing !== null || prompt.trim() !== "";
  const canSave = promptReady && !busy && uploads.allUploaded;
  const canGenerate = promptReady && !busy && uploads.allIndexed;

  const close = () => {
    uploads.reset();
    onOpenChange(false);
  };

  const changeGoal = (next: string) => {
    setKpiId(next);
    setAttached([]);
    if (next !== NEW_GOAL) {
      listKpiDocuments(next)
        .then(setAttached)
        .catch(() => setAttached([]));
    }
  };

  const detach = async (docId: string) => {
    if (!existing) return;
    try {
      await detachKpiDocument(existing.id, docId);
      setAttached((prev) => prev.filter((d) => d.id !== docId));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("wizard.detachError"));
    }
  };

  const ensureKpi = async (): Promise<{ kpi: ApiKPI; constraints: string }> => {
    const text = prompt.trim();
    if (editKpi) {
      if (text === savedPrompt.current.trim() || text === "") {
        return { kpi: editKpi, constraints: "" };
      }
      const { constraints, ...body } = await parsedBody(text);
      const updated = await updateKPI(editKpi.id, {
        ...body,
        status: editKpi.status,
        detail: editKpi.detail ?? {},
      });
      onUpdated?.(updated);
      return { kpi: updated, constraints };
    }
    if (existing) return { kpi: existing, constraints: text };
    const { constraints, ...body } = await parsedBody(text);
    const created = await createKPI(body);
    onCreated?.(created);
    return { kpi: created, constraints };
  };

  const submit = async (generate: boolean) => {
    setBusy(true);
    try {
      const { kpi, constraints } = await ensureKpi();
      if (uploads.docIds.length > 0) {
        await attachKpiDocuments(kpi.id, uploads.docIds);
      }
      toast.success(
        editKpi ? t("toast.updated") : existing ? t("wizard.attachedToast") : t("toast.created"),
      );
      uploads.reset();
      onOpenChange(false);
      if (generate) {
        void runTask({
          key: `kpi:${kpi.id}:generate`,
          title: t("task.generateTitle"),
          description: kpi.title,
          successMessage: (created: ApiHypothesis[]) =>
            t("task.generateSuccess", { count: created.length }),
          errorMessage: t("task.generateError"),
          run: () =>
            generateHypotheses({
              kpi_id: kpi.id,
              constraints: constraints || undefined,
              count: clampGenerateCount(String(count)),
            }),
          onSuccess: onGenerated,
        });
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("toast.createError"));
    } finally {
      setBusy(false);
    }
  };

  const hasFiles = attached.length > 0 || uploads.items.length > 0;

  return (
    <Dialog open={open} onOpenChange={(next) => (next ? onOpenChange(true) : close())}>
      <DialogContent
        className="max-h-[calc(100dvh-3rem)] max-w-2xl overflow-y-auto"
        onDragEnter={(e) => {
          if (!e.dataTransfer.types.includes("Files")) return;
          dragDepth.current += 1;
          setDragOver(true);
        }}
        onDragOver={(e) => {
          if (e.dataTransfer.types.includes("Files")) e.preventDefault();
        }}
        onDragLeave={() => {
          dragDepth.current = Math.max(0, dragDepth.current - 1);
          if (dragDepth.current === 0) setDragOver(false);
        }}
        onDrop={(e) => {
          if (e.dataTransfer.files.length === 0) return;
          e.preventDefault();
          dragDepth.current = 0;
          setDragOver(false);
          uploads.addFiles(e.dataTransfer.files);
        }}
      >
        {dragOver && (
          <div className="pointer-events-none absolute inset-0 z-10 flex flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed border-brand-border bg-brand-wash/95 backdrop-blur-sm">
            <Paperclip className="size-7 text-brand" aria-hidden />
            <p className="text-sm font-medium text-foreground">{t("wizard.dropHere")}</p>
          </div>
        )}
        <DialogHeader>
          <DialogTitle>{editKpi ? t("wizard.editTitle") : t("wizard.title")}</DialogTitle>
          <DialogDescription>
            {editKpi ? t("wizard.editDescription") : t("wizard.description")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {!editKpi && activeKpis.length > 0 && (
            <div className="space-y-1.5">
              <Label htmlFor="goal-select">{t("wizard.goal")}</Label>
              <Select value={kpiId} onValueChange={changeGoal}>
                <SelectTrigger id="goal-select">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NEW_GOAL}>{t("wizard.newGoal")}</SelectItem>
                  {activeKpis.map((k) => (
                    <SelectItem key={k.id} value={k.id}>
                      {k.title}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}

          <div className="space-y-1.5">
            <Label htmlFor="goal-prompt">
              {existing && !editKpi ? t("wizard.promptExisting") : t("wizard.prompt")}
            </Label>
            <textarea
              id="goal-prompt"
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              rows={8}
              className="flex w-full resize-none rounded-lg border border-input bg-card px-3 py-2 text-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
              placeholder={
                existing && !editKpi
                  ? t("wizard.promptExistingPlaceholder")
                  : t("wizard.promptPlaceholder")
              }
            />
            {!existing && <p className="text-xs text-muted-foreground">{t("wizard.promptHint")}</p>}
            {editKpi && <p className="text-xs text-muted-foreground">{t("wizard.editHint")}</p>}
          </div>

          <div className="space-y-1.5">
            <Label>{t("wizard.filesLabel")}</Label>
            {hasFiles && (
              <ul className="space-y-1.5">
                {attached.map((d) => (
                  <li
                    key={d.id}
                    className="flex items-center gap-2 rounded-lg border bg-secondary/40 px-3 py-2 text-sm"
                  >
                    <FileText className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                    <span className="min-w-0 flex-1 truncate">{d.title || d.filename}</span>
                    {d.status === "indexed" ? (
                      <CircleCheck className="size-4 shrink-0 text-ok" aria-hidden />
                    ) : (
                      <span className="shrink-0 text-xs text-muted-foreground">
                        {t("wizard.fileIndexing")}
                      </span>
                    )}
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={t("wizard.fileRemove")}
                      onClick={() => void detach(d.id)}
                    >
                      <X className="size-3.5" aria-hidden />
                    </Button>
                  </li>
                ))}
                {uploads.items.map((it) => (
                  <AttachedFileRow key={it.key} item={it} onRemove={() => uploads.remove(it.key)} />
                ))}
              </ul>
            )}
            <button
              type="button"
              onClick={() => fileInput.current?.click()}
              className="flex w-full cursor-pointer flex-col items-center justify-center gap-1.5 rounded-lg border border-dashed px-3 py-6 text-sm text-muted-foreground transition-colors hover:border-brand-border hover:bg-brand-wash focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Paperclip className="size-6" aria-hidden />
              <span className="font-medium text-foreground">
                {hasFiles ? t("wizard.addMoreFiles") : t("wizard.addFiles")}
              </span>
              <input
                ref={fileInput}
                type="file"
                multiple
                accept={UPLOAD_ACCEPT}
                className="hidden"
                onChange={(e) => {
                  if (e.target.files) uploads.addFiles(e.target.files);
                  e.target.value = "";
                }}
              />
            </button>
            <p className="text-xs text-muted-foreground">{t("wizard.filesNote")}</p>
          </div>
        </div>

        <DialogFooter className="items-end gap-3 sm:justify-between">
          <div className="space-y-1.5">
            <Label htmlFor="goal-count" className="text-xs text-muted-foreground">
              {t("wizard.count")}
            </Label>
            <Input
              id="goal-count"
              type="number"
              min={GENERATION_COUNT_MIN}
              max={GENERATION_COUNT_MAX}
              value={count}
              onChange={(e) => setCount(clampGenerateCount(e.target.value))}
              className="w-20"
            />
          </div>
          <div className="flex flex-wrap justify-end gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={!canSave}
              onClick={() => void submit(false)}
            >
              {editKpi ? t("wizard.save") : existing ? t("wizard.attach") : t("wizard.create")}
            </Button>
            <Button
              variant="brand"
              size="sm"
              disabled={!canGenerate}
              onClick={() => void submit(true)}
            >
              <Wand2 className="size-4" aria-hidden />
              {busy
                ? t("wizard.busy")
                : editKpi
                  ? t("wizard.saveAndGenerate")
                  : existing
                    ? t("wizard.generate")
                    : t("wizard.createAndGenerate")}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function AttachedFileRow({ item, onRemove }: { item: AttachedUpload; onRemove: () => void }) {
  const { t } = useTranslation("kpi");
  return (
    <li className="flex items-center gap-2 rounded-lg border bg-secondary/40 px-3 py-2 text-sm">
      <FileText className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 flex-1 truncate">{item.name}</span>
      {item.phase === "uploading" && (
        <span className="shrink-0 font-mono text-xs text-muted-foreground">
          {Math.round(item.fraction * 100)}%
        </span>
      )}
      {item.phase === "indexing" && (
        <span className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
          <Spinner size="sm" />
          {t("wizard.fileIndexing")}
        </span>
      )}
      {item.phase === "indexed" && item.alreadyExisted && (
        <Badge variant="secondary" className="shrink-0">
          {t("wizard.fileAlreadyInBase")}
        </Badge>
      )}
      {item.phase === "indexed" && !item.alreadyExisted && (
        <CircleCheck className="size-4 shrink-0 text-ok" aria-hidden />
      )}
      {item.phase === "failed" && (
        <span className="shrink-0 text-xs text-destructive">
          {item.error || t("wizard.fileFailed")}
        </span>
      )}
      <Button variant="ghost" size="icon-sm" aria-label={t("wizard.fileRemove")} onClick={onRemove}>
        <X className="size-3.5" aria-hidden />
      </Button>
    </li>
  );
}
