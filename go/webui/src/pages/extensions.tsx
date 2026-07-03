import { useQuery } from "@tanstack/react-query";
import { Puzzle } from "lucide-react";

import { backend } from "@/lib/backend";
import { useLocale } from "@/lib/i18n";
import { Badge } from "@/components/ui/badge";

type LoadedExtension = {
  id: string;
  capability: string;
};

type GoExtensionsResponse = {
  count: number;
};

const CAPABILITY_ORDER = [
  "phase_hook",
  "request_hook",
  "response_hook",
  "provider_vendor",
  "protocol_endpoint",
] as const;

const CAPABILITY_LABELS: Record<string, { zh: string; en: string }> = {
  phase_hook: { zh: "阶段 Hook", en: "Phase Hooks" },
  request_hook: { zh: "请求 Hook", en: "Request Hooks" },
  response_hook: { zh: "响应 Hook", en: "Response Hooks" },
  provider_vendor: { zh: "提供商 Vendor", en: "Provider Vendors" },
  protocol_endpoint: { zh: "协议端点", en: "Protocol Endpoints" },
};

export default function ExtensionsPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";

  const { data, isLoading, isError } = useQuery({
    queryKey: ["loaded-extensions"],
    queryFn: () => backend<GoExtensionsResponse>("get_loaded_extensions"),
  });

  const extensions: LoadedExtension[] = [];
  const extensionCount = data?.count ?? 0;

  const grouped = new Map<string, LoadedExtension[]>();
  for (const ext of extensions) {
    const bucket = grouped.get(ext.capability) ?? [];
    bucket.push(ext);
    grouped.set(ext.capability, bucket);
  }

  const orderedCapabilities = [
    ...CAPABILITY_ORDER.filter((cap) => grouped.has(cap)),
    ...[...grouped.keys()].filter(
      (cap) => !CAPABILITY_ORDER.includes(cap as (typeof CAPABILITY_ORDER)[number]),
    ),
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-slate-900">
            {isZh ? "已加载扩展" : "Loaded Extensions"}
          </h1>
          <p className="mt-1 text-sm text-slate-500">
            {isZh
              ? "编译期注册的内置扩展(只读)"
              : "Compile-time registered built-in extensions (read-only)"}
          </p>
        </div>
        <Badge variant="secondary" className="text-xs">
          {isZh ? `共 ${extensionCount} 项` : `${extensionCount} total`}
        </Badge>
      </div>

      {isLoading && (
        <div className="glass rounded-2xl p-6 text-sm text-slate-500">
          {isZh ? "加载中..." : "Loading..."}
        </div>
      )}

      {isError && (
        <div className="glass rounded-2xl p-6 text-sm text-red-500">
          {isZh ? "加载扩展列表失败" : "Failed to load extensions"}
        </div>
      )}

      {!isLoading && !isError && extensionCount === 0 && (
        <div className="glass rounded-2xl p-6 text-sm text-slate-500">
          {isZh ? "暂无已加载扩展" : "No extensions loaded"}
        </div>
      )}

      {!isLoading &&
        !isError &&
        orderedCapabilities.map((capability) => {
          const items = grouped.get(capability) ?? [];
          const label = CAPABILITY_LABELS[capability];
          const title = label ? (isZh ? label.zh : label.en) : capability;
          return (
            <div key={capability} className="glass rounded-2xl p-4">
              <div className="mb-3 flex items-center gap-2">
                <Puzzle className="h-4 w-4 text-slate-500" />
                <h2 className="text-sm font-semibold text-slate-900">{title}</h2>
                <Badge variant="outline" className="text-[10px]">
                  {items.length}
                </Badge>
              </div>
              <div className="flex flex-wrap gap-2">
                {items.map((ext) => (
                  <span
                    key={`${capability}:${ext.id}`}
                    className="rounded-lg border border-slate-200/80 bg-white/60 px-2.5 py-1 font-mono text-xs text-slate-700"
                  >
                    {ext.id}
                  </span>
                ))}
              </div>
            </div>
          );
        })}
    </div>
  );
}
