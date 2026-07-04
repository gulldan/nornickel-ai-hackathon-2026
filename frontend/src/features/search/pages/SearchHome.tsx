import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { motion } from "framer-motion";
import { History, TrendingUp } from "lucide-react";
import { SearchBar } from "@/features/search/ui/SearchBar";
import { Chip } from "@/shared/ui/Chip";
import { Kicker } from "@/shared/ui/Kicker";
import { useAuth } from "@/features/auth";
import { listChats } from "@/features/search/api";
import { listClusters } from "@/features/cluster";
import { cleanInternalTitle } from "@/features/hypothesis";

interface StarterTopic {
  label: string;
  query: string;
}

/** Recent queries = the user's latest chats (a chat's title is the query text). */
function useRecentQueries(limit: number): string[] {
  const [recent, setRecent] = useState<string[]>([]);
  useEffect(() => {
    let cancelled = false;
    // Сервер отдаёт новые первыми; берём с запасом на дубли заголовков.
    listChats({ limit: limit * 4 })
      .then((page) => {
        if (cancelled) return;
        const unique: string[] = [];
        for (const c of page.items) {
          const title = c.title.trim();
          if (title && !unique.includes(title)) unique.push(title);
          if (unique.length >= limit) break;
        }
        setRecent(unique);
      })
      .catch(() => {
        // The «Недавние запросы» (Recent queries) block is auxiliary — on error we just hide it.
        if (!cancelled) setRecent([]);
      });
    return () => {
      cancelled = true;
    };
  }, [limit]);
  return recent;
}

function useStarterTopics(limit: number): StarterTopic[] {
  const [topics, setTopics] = useState<StarterTopic[]>([]);
  useEffect(() => {
    let cancelled = false;
    listClusters()
      .then((clusters) => {
        if (cancelled) return;
        const next = clusters
          .toSorted((a, b) => b.document_count - a.document_count)
          .map((c) => {
            const label = cleanInternalTitle(c.label);
            const query = [label, ...(c.keywords ?? []).slice(0, 3)].filter(Boolean).join(" ");
            return label && query ? { label, query } : null;
          })
          .filter((v): v is StarterTopic => v !== null)
          .slice(0, limit);
        setTopics(next);
      })
      .catch(() => {
        if (!cancelled) setTopics([]);
      });
    return () => {
      cancelled = true;
    };
  }, [limit]);
  return topics;
}

export function SearchHome() {
  const { t } = useTranslation("search");
  const navigate = useNavigate();
  const { auth } = useAuth();
  const go = (q: string) => navigate(`/search?q=${encodeURIComponent(q)}`);
  const recent = useRecentQueries(6);
  const starterTopics = useStarterTopics(6);
  const rawName = auth?.user.username ?? "";
  const username = rawName ? rawName.charAt(0).toUpperCase() + rawName.slice(1) : "";
  return (
    <div className="flex min-h-[calc(100vh-0px)] w-full flex-col items-center justify-center bg-background px-4 py-16">
      <motion.div
        initial={{
          opacity: 0,
          y: 12,
        }}
        animate={{
          opacity: 1,
          y: 0,
        }}
        transition={{
          duration: 0.35,
          ease: "easeOut",
        }}
        className="w-full max-w-3xl"
      >
        <h1 className="font-display text-balance text-center text-4xl md:text-5xl">
          {username ? t("home.greetingNamed", { name: username }) : t("home.greeting")}
        </h1>
        <p className="mt-3 text-center text-muted-foreground">{t("home.subtitle")}</p>
        <div className="mt-7">
          <SearchBar size="lg" autoFocus onSearch={go} />
        </div>

        {starterTopics.length > 0 && (
          <div className="mt-8">
            <Kicker className="mb-2.5 flex items-center gap-1.5">
              <TrendingUp className="size-3.5" aria-hidden />
              {t("home.starterTopics")}
            </Kicker>
            <div className="flex flex-wrap gap-2">
              {starterTopics.map((topic) => (
                <Chip key={topic.label} onClick={() => go(topic.query)}>
                  {topic.label}
                </Chip>
              ))}
            </div>
          </div>
        )}

        {recent.length > 0 && (
          <div className="mt-6">
            <Kicker className="mb-2.5 flex items-center gap-1.5">
              <History className="size-3.5" aria-hidden />
              {t("home.recentQueries")}
            </Kicker>
            {/* Сетка компактных карточек (как подсказки в ChatGPT/Claude):
                длинные запросы обрезаются, блок не уезжает за экран. */}
            <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-2">
              {recent.map((q) => (
                <button
                  key={q}
                  type="button"
                  title={q}
                  onClick={() => go(q)}
                  className="flex min-w-0 items-center gap-2.5 rounded-lg border bg-card px-3 py-2 text-left text-sm text-foreground transition-colors hover:border-brand-border hover:bg-secondary/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <History className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                  <span className="truncate">{q}</span>
                </button>
              ))}
            </div>
          </div>
        )}
      </motion.div>
    </div>
  );
}
