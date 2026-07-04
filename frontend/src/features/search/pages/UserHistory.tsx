import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { History as HistoryIcon } from "lucide-react";
import {
  Badge,
  EmptyState,
  ErrorState,
  ListSkeleton,
  PageHeader,
  Pagination,
  SearchField,
  Segmented,
} from "@/shared/ui";
import { listChats, ApiChat } from "@/features/search/api";
import { useAuth } from "@/features/auth";
import { formatDateTime } from "@/shared/lib/format";

type Phase = "loading" | "ready" | "error";
type Scope = "mine" | "all";

const PAGE = 20;

// Человекочитаемые подписи страниц, с которых был задан вопрос, — в словарях.
const SOURCE_KEYS = {
  search: "history.source.search",
} as const;

export function UserHistory() {
  const { t } = useTranslation("search");
  const navigate = useNavigate();
  const { roles } = useAuth();
  const isAdmin = roles.includes("admin");

  const [scope, setScope] = useState<Scope>("mine");
  const [page, setPage] = useState(1);
  const [chats, setChats] = useState<ApiChat[]>([]);
  const [total, setTotal] = useState(0);
  const [phase, setPhase] = useState<Phase>("loading");
  const [filter, setFilter] = useState("");

  const load = useCallback(async (nextPage: number, nextScope: Scope) => {
    setPhase("loading");
    try {
      const result = await listChats({
        limit: PAGE,
        offset: (nextPage - 1) * PAGE,
        all: nextScope === "all",
      });
      setChats(result.items);
      setTotal(result.total);
      setPhase("ready");
    } catch {
      setPhase("error");
    }
  }, []);

  useEffect(() => {
    void load(page, scope);
  }, [load, page, scope]);

  const pageCount = Math.max(1, Math.ceil(total / PAGE));
  const filtered = chats.filter((c) => c.title.toLowerCase().includes(filter.toLowerCase()));
  // Открываем сохранённый диалог с его ответами и источниками — без повторного
  // запроса к модели (тот же вопрос мог бы дать другой ответ).
  const open = (chat: ApiChat) => navigate(`/search?chat=${encodeURIComponent(chat.id)}`);

  return (
    <div className="mx-auto w-full max-w-3xl px-4 py-6 md:py-8">
      <PageHeader
        kicker={t("history.kicker")}
        title={t("history.title")}
        description={t("history.description")}
      />

      <div className="mt-5 flex flex-wrap items-center gap-2">
        <SearchField
          className="min-w-56 flex-1"
          value={filter}
          onChange={setFilter}
          placeholder={t("history.filterPlaceholder")}
          ariaLabel={t("history.filterAriaLabel")}
        />
        {isAdmin && (
          <Segmented
            aria-label={t("history.scopeAriaLabel")}
            value={scope}
            onChange={(v) => {
              setScope(v);
              setPage(1);
            }}
            options={[
              { value: "mine", label: t("history.scopeMine") },
              { value: "all", label: t("history.scopeAll") },
            ]}
          />
        )}
      </div>

      {phase === "loading" && <ListSkeleton rows={4} ariaLabel={t("history.loadingAriaLabel")} />}

      {phase === "error" && (
        <ErrorState message={t("history.loadError")} onRetry={() => void load(page, scope)} />
      )}

      {phase === "ready" &&
        (filtered.length === 0 ? (
          <EmptyState
            icon={HistoryIcon}
            title={chats.length === 0 ? t("history.emptyTitle") : t("history.notFoundTitle")}
            description={
              chats.length === 0 ? t("history.emptyDescription") : t("history.notFoundDescription")
            }
          />
        ) : (
          <>
            <p className="mt-4 text-sm text-muted-foreground">
              {filter.trim() === ""
                ? t("history.pageInfo", { count: total, page, pageCount })
                : t("history.foundOnPage", { count: filtered.length })}
            </p>
            <ul className="mt-3 space-y-2">
              {filtered.map((chat) => (
                <li key={chat.id}>
                  <button
                    type="button"
                    aria-label={t("history.openChatAriaLabel", { title: chat.title })}
                    onClick={() => open(chat)}
                    className="flex w-full cursor-pointer items-center gap-3 rounded-xl border bg-card px-4 py-3 text-left transition-colors hover:border-brand-border focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm font-medium">{chat.title}</p>
                      <p className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
                        <span>{formatDateTime(chat.created_at)}</span>
                        {chat.owner_username && (
                          <span>· {t("history.byUser", { name: chat.owner_username })}</span>
                        )}
                      </p>
                    </div>
                    <Badge variant="secondary" className="shrink-0">
                      {t(
                        SOURCE_KEYS[chat.source as keyof typeof SOURCE_KEYS] ?? SOURCE_KEYS.search,
                      )}
                    </Badge>
                  </button>
                </li>
              ))}
            </ul>
            <Pagination className="mt-5" page={page} pageCount={pageCount} onPage={setPage} />
          </>
        ))}
    </div>
  );
}
