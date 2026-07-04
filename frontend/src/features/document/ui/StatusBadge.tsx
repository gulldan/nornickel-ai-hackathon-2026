import { Badge } from "@/shared/ui/Badge";
import { i18n } from "@/shared/i18n";
import { cn } from "@/shared/lib/cn";
import { DocStatus } from "@/features/document/api";

/** Единственный источник названий стадий конвейера документа — им пользуются
 *  и таблица документов, и админ-дашборд (раньше названия расходились).
 *  Тексты в словаре document (status.*); геттеры сохраняют доступ по ключу. */
const STATUS_LABELS: Record<DocStatus, string> = {
  get uploaded() {
    return i18n.t("document:status.uploaded");
  },
  get queued() {
    return i18n.t("document:status.queued");
  },
  get parsing() {
    return i18n.t("document:status.parsing");
  },
  get ocr() {
    return i18n.t("document:status.ocr");
  },
  get parsed() {
    return i18n.t("document:status.parsed");
  },
  get chunking() {
    return i18n.t("document:status.chunking");
  },
  get indexed() {
    return i18n.t("document:status.indexed");
  },
  get failed() {
    return i18n.t("document:status.failed");
  },
};

/** Порядковый номер стадии на пятишаговой шкале конвейера. */
function stepOf(status: DocStatus): number {
  switch (status) {
    case "uploaded":
      return 0;
    case "queued":
      return 1;
    case "parsing":
    case "ocr":
      return 2;
    case "parsed":
    case "chunking":
      return 3;
    case "indexed":
      return 4;
    case "failed":
      return -1;
  }
}

const STEPS_TOTAL = 5;

/** Степпер конвейера: пять сегментов «Загружен → В очереди → Текст →
 *  Подготовка → Готов» + название текущей стадии и прогресс внутри неё
 *  («стр. 3/12» у распознавания). Ошибка — красный бейдж с той же геометрией. */
export function PipelineStepper({
  status,
  statusMsg,
  progress,
  className,
}: {
  status: DocStatus;
  statusMsg?: string;
  progress?: { current: number; total: number };
  className?: string;
}) {
  const step = stepOf(status);
  if (step < 0) {
    return (
      <span className={cn("inline-flex max-w-full items-center gap-2", className)}>
        <Badge variant="risk">{STATUS_LABELS.failed}</Badge>
        {statusMsg && (
          <span className="truncate text-xs text-muted-foreground" title={statusMsg}>
            {statusMsg}
          </span>
        )}
      </span>
    );
  }
  const done = status === "indexed";
  return (
    <span className={cn("inline-flex items-center gap-2.5", className)}>
      <span className="flex items-center gap-[3px]" aria-hidden>
        {Array.from({ length: STEPS_TOTAL }, (_, i) => (
          <span
            key={i}
            className={cn(
              "h-2 w-3 rounded-[2px]",
              i < step && "bg-ok",
              i === step && (done ? "bg-ok" : "animate-pulse bg-brand"),
              i > step && "bg-border",
            )}
          />
        ))}
      </span>
      <span className={cn("whitespace-nowrap text-xs", done ? "text-ok" : "text-foreground")}>
        {STATUS_LABELS[status]}
        {progress && progress.total > 0 && (
          <span className="ml-1 font-mono text-muted-foreground">
            {progress.current}/{progress.total}
          </span>
        )}
      </span>
    </span>
  );
}
