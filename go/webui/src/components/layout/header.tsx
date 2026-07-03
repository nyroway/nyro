import { RefreshCw } from "lucide-react";
import { cn } from "@/lib/utils";
import { MoonStar, Languages } from "lucide-react";

interface HeaderProps {
  title: string;
  subtitle?: string;
  actions?: import("react").ReactNode;
  onRefresh?: () => void;
  isRefreshing?: boolean;
}

export function Header({
  title,
  subtitle,
  actions,
  onRefresh,
  isRefreshing,
}: HeaderProps) {
  return (
    <header className="glass-strong sticky top-4 z-30 rounded-[1.25rem] px-5 py-3">
      <div className="flex items-center justify-between gap-4">
        <div className="min-w-0">
          <h1 className="truncate text-[18px] font-semibold text-slate-900">
            {title}
          </h1>
          {subtitle && (
            <p className="truncate text-sm text-slate-500">{subtitle}</p>
          )}
        </div>

        <div className="flex items-center gap-2">
          <button className="inline-flex h-9 w-9 items-center justify-center rounded-full border border-white/70 bg-white/70 text-slate-600 transition-colors hover:text-slate-900">
            <MoonStar className="h-4 w-4" />
          </button>
          <button className="inline-flex h-9 w-9 items-center justify-center rounded-full border border-white/70 bg-white/70 text-slate-600 transition-colors hover:text-slate-900">
            <Languages className="h-4 w-4" />
          </button>
          {onRefresh && (
            <button
              onClick={onRefresh}
              disabled={isRefreshing}
              className="inline-flex h-9 w-9 items-center justify-center rounded-full border border-white/70 bg-white/70 text-slate-600 transition-colors hover:text-slate-900 cursor-pointer disabled:opacity-50"
            >
              <RefreshCw
                className={cn("h-4 w-4", isRefreshing && "animate-spin")}
              />
            </button>
          )}
          {actions}
        </div>
      </div>
    </header>
  );
}
