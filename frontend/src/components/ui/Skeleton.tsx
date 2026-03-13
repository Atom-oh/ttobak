export function SkeletonText({ width = 'w-full' }: { width?: string }) {
  return (
    <div className={`h-4 ${width} bg-slate-200 dark:bg-slate-700 rounded animate-pulse`} />
  );
}

export function SkeletonCard() {
  return (
    <div className="bg-white dark:bg-slate-800 border border-slate-100 dark:border-slate-700 p-4 lg:p-6 rounded-xl shadow-sm">
      <div className="flex justify-between items-start mb-4">
        <SkeletonText width="w-2/3" />
        <div className="w-16 h-5 bg-slate-200 dark:bg-slate-700 rounded-full animate-pulse" />
      </div>
      <div className="flex items-center gap-2 mb-3">
        <div className="w-3.5 h-3.5 bg-slate-200 dark:bg-slate-700 rounded animate-pulse" />
        <SkeletonText width="w-40" />
      </div>
      <div className="space-y-2 mb-4">
        <SkeletonText />
        <SkeletonText width="w-4/5" />
      </div>
      <div className="flex items-center justify-between pt-4 border-t border-slate-100 dark:border-slate-800">
        <div className="flex -space-x-2">
          {[0, 1, 2].map((i) => (
            <div key={i} className="size-6 lg:size-7 rounded-full bg-slate-200 dark:bg-slate-700 border-2 border-white dark:border-slate-800 animate-pulse" />
          ))}
        </div>
        <div className="w-6 h-6 bg-slate-200 dark:bg-slate-700 rounded animate-pulse" />
      </div>
    </div>
  );
}
