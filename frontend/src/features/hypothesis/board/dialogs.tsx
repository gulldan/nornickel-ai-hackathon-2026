import { Copy, Download } from "lucide-react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { downloadFile } from "@/features/hypothesis/model";
import { Button } from "@/shared/ui/Button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/Dialog";
import { RichText } from "@/shared/ui/RichText";

export function DigestDialog({
  open,
  onOpenChange,
  digest,
  count,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  digest: string;
  count: number;
}) {
  const { t } = useTranslation("hypothesis");
  const { t: tCommon } = useTranslation("common");
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("digest.dialogTitle", { n: count })}</DialogTitle>
          <DialogDescription>{t("digest.dialogDescription")}</DialogDescription>
        </DialogHeader>
        <div className="max-h-[58vh] overflow-y-auto rounded-lg border bg-secondary/40 p-4">
          <RichText className="text-sm">{digest}</RichText>
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              void navigator.clipboard.writeText(digest);
              toast.success(t("digest.copied"));
            }}
          >
            <Copy className="size-4" aria-hidden />
            {tCommon("actions.copy")}
          </Button>
          <Button
            size="sm"
            onClick={() =>
              downloadFile(t("digest.fileName"), digest, "text/markdown;charset=utf-8")
            }
          >
            <Download className="size-4" aria-hidden />
            {t("digest.downloadMd")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
