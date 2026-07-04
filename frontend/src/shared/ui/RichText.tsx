import { lazy, Suspense } from "react";
import type { Components } from "react-markdown";

import { cn } from "@/shared/lib/cn";

const RichTextInner = lazy(() => import("./RichTextInner"));

interface RichTextProps {
  /** Markdown (GFM) with optional LaTeX math ($…$, $$…$$) and chemistry (\ce{}). */
  children: string | null | undefined;
  className?: string;
  /** Point overrides for Markdown nodes (e.g. citation footnotes in answers). */
  components?: Components;
}

/**
 * Renders LLM-authored scientific text: Markdown (GFM tables/lists/links) plus
 * inline/display LaTeX math and chemistry via KaTeX. The renderer is loaded
 * lazily; until it arrives the raw text is shown (no spinner flash), then it
 * upgrades in place. Markdown is parsed to React elements — raw HTML is NOT
 * enabled, so corpus/model output cannot inject markup.
 */
export function RichText({ children, className, components }: RichTextProps) {
  const text = (children ?? "").trim();
  if (text === "") {
    return null;
  }
  return (
    <Suspense
      fallback={<div className={cn("rich-text whitespace-pre-wrap", className)}>{text}</div>}
    >
      <RichTextInner className={className} components={components}>
        {text}
      </RichTextInner>
    </Suspense>
  );
}
