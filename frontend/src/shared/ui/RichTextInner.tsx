import { memo } from "react";
import Markdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import rehypeKatex from "rehype-katex";
// mhchem teaches KaTeX \ce{…} / \pu{…} for chemical formulae and units
// (materials / oil & gas) — imported for its side effect before rendering.
import "katex/contrib/mhchem";
import "katex/dist/katex.min.css";

import { cn } from "@/shared/lib/cn";

interface RichTextInnerProps {
  children: string;
  className?: string;
  components?: Components;
}

// The heavy half of <RichText> (react-markdown + KaTeX). Loaded lazily so the
// ~726KB of markdown/math code stays off the initial bundle and only arrives
// when a screen actually renders rich text. Default export for React.lazy.
// Memoized: re-parsing markdown + re-rendering KaTeX on unrelated parent
// re-renders is the single biggest main-thread cost of long answer threads.
function RichTextInner({ children, className, components }: RichTextInnerProps) {
  return (
    <div className={cn("rich-text", className)}>
      <Markdown
        remarkPlugins={[remarkGfm, remarkMath]}
        rehypePlugins={[rehypeKatex]}
        components={components}
      >
        {children}
      </Markdown>
    </div>
  );
}

export default memo(RichTextInner);
