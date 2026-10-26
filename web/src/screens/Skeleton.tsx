export function Skeleton() {
  return (
    <div aria-hidden="true" data-testid="list-skeleton">
      {Array.from({ length: 7 }, (_, i) => (
        <div key={i} className="flex min-h-28 gap-3 border-b border-line px-4 py-3">
          <div className="flex flex-1 flex-col gap-2">
            <div className="h-3 w-1/3 rounded bg-surface" />
            <div className="h-4 w-11/12 rounded bg-surface" />
            <div className="h-4 w-3/4 rounded bg-surface" />
            <div className="h-3 w-2/3 rounded bg-surface" />
          </div>
          <div className="size-[var(--thumb)] rounded-lg bg-surface" />
        </div>
      ))}
    </div>
  );
}
