import { Skeleton } from "@/shared/ui/Skeleton";

interface CardGridSkeletonProps {
  /** Number of placeholder cards to render. */
  count?: number;
}

/** Loading placeholder for a responsive grid of cards (e.g. the hypotheses
 *  board). Matches the card padding/spacing used on those pages. */
export function CardGridSkeleton({ count = 6 }: CardGridSkeletonProps) {
  return (
    <div className="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className="space-y-3 rounded-lg border bg-card p-6">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-5 w-3/4" />
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-2/3" />
        </div>
      ))}
    </div>
  );
}
