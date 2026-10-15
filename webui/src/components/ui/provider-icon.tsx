import { useEffect, useState } from "react";
import { cn } from "@/lib/utils";

const iconModules = import.meta.glob("../../assets/icons/*.svg", {
  query: "?raw",
  import: "default",
}) as Record<string, () => Promise<string>>;

const iconLoaderMap: Record<string, () => Promise<string>> = {};
for (const [path, loader] of Object.entries(iconModules)) {
  const matched = path.match(/\/([^/]+)\.svg$/);
  if (matched?.[1]) {
    iconLoaderMap[matched[1].toLowerCase()] = loader;
  }
}
const iconMarkupCache = new Map<string, string>();

interface ProviderIconProps {
  iconKey?: string;
  name?: string;
  protocol?: string;
  baseUrl?: string;
  size?: number;
  className?: string;
  monochrome?: boolean;
  fill?: boolean;
}

const ICON_ALIASES: Record<string, string> = {
  custom: "nyro",
  claude: "anthropic",
  chatgpt: "openai",
  gpt: "openai",
  googleai: "google",
  googleapis: "google",
  generativelanguage: "gemini",
  tongyi: "qwen",
  dashscope: "qwen",
  modelscope: "modelscope-color",
  aihubmix: "aihubmix-color",
  longcat: "longcat-color",
  moonshot: "kimi",
  hunyuan: "tencent",
  glm: "zhipu",
  chatglm: "zhipu",
};

function tokenize(value?: string): string[] {
  if (!value) return [];
  return value
    .toLowerCase()
    .split(/[^a-z0-9]+/g)
    .map((s) => s.trim())
    .filter(Boolean);
}

function hostTokens(baseUrl?: string): string[] {
  if (!baseUrl) return [];
  try {
    return tokenize(new URL(baseUrl).hostname);
  } catch {
    return tokenize(baseUrl);
  }
}

function normalizeToken(token: string): string {
  return ICON_ALIASES[token] ?? token;
}

export function resolveProviderIconKey({
  name,
  protocol,
  baseUrl,
}: {
  name?: string;
  protocol?: string;
  baseUrl?: string;
}): string | null {
  const raw = [...tokenize(name), ...hostTokens(baseUrl), ...tokenize(protocol)];
  const candidates = raw.flatMap((token) => {
    const normalized = normalizeToken(token);
    return normalized === token ? [token] : [normalized, token];
  });

  for (const key of candidates) {
    if (key in iconLoaderMap) return key;
  }
  return null;
}

export function ProviderIcon({
  iconKey,
  name,
  protocol,
  baseUrl,
  size = 20,
  className,
  monochrome = false,
  fill = false,
}: ProviderIconProps) {
  const resolvedIconKey =
    iconKey && iconLoaderMap[iconKey.toLowerCase()]
      ? iconKey.toLowerCase()
      : resolveProviderIconKey({ name, protocol, baseUrl });
  const [iconMarkup, setIconMarkup] = useState<string>("");
  const fallback = (name || protocol || "?").slice(0, 1).toUpperCase();

  useEffect(() => {
    if (!resolvedIconKey) {
      setIconMarkup("");
      return;
    }
    const cacheKey = `${resolvedIconKey}:${monochrome ? "mono" : "color"}`;
    const cached = iconMarkupCache.get(cacheKey);
    if (cached) {
      setIconMarkup(cached);
      return;
    }
    const loader = iconLoaderMap[resolvedIconKey];
    if (!loader) {
      setIconMarkup("");
      return;
    }
    let cancelled = false;
    loader()
      .then((rawSvg) => {
        if (cancelled) return;
        const markup = normalizeProviderSvg(rawSvg, monochrome, resolvedIconKey);
        iconMarkupCache.set(cacheKey, markup);
        setIconMarkup(markup);
      })
      .catch(() => {
        if (!cancelled) setIconMarkup("");
      });

    return () => {
      cancelled = true;
    };
  }, [resolvedIconKey, monochrome]);

  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center justify-center overflow-hidden rounded-md border border-slate-200 bg-white/85 text-[10px] font-semibold text-slate-500",
        className,
      )}
      style={{ width: size, height: size }}
      title={name || protocol || "provider"}
    >
      {iconMarkup ? (
        <span
          aria-hidden="true"
          className={cn("provider-icon-markup", fill ? "h-full w-full" : "h-[78%] w-[78%]")}
          dangerouslySetInnerHTML={{ __html: iconMarkup }}
        />
      ) : (
        fallback
      )}
    </span>
  );
}

function normalizeProviderSvg(svg: string, monochrome: boolean, iconKey?: string | null) {
  let next = svg
    .replace(/<title>.*?<\/title>/gis, "")
    .replace(
      /<svg\b([^>]*)>/i,
      '<svg$1 class="provider-icon-svg" aria-hidden="true" focusable="false">',
    );

  if (!monochrome) return next;

  if (iconKey === "zai") {
    return next
      .replace(/<defs>[\s\S]*?<\/defs>/gi, "")
      .replace(/\sstyle="[^"]*"/gi, "")
      .replace(
        /(<path[^>]*id="zai-bg"[^>]*?)\sfill="[^"]*"/i,
        '$1 fill="none"',
      )
      .replace(
        /(<path[^>]*id="zai-bg"[^>]*?)\sstroke="[^"]*"/i,
        '$1 stroke="currentColor"',
      )
      .replace(
        /(<path[^>]*id="zai-bg"[^>]*?)\sstroke-width="[^"]*"/i,
        '$1 stroke-width="2.1"',
      )
      .replace(
        /(<g[^>]*id="zai-glyph"[^>]*?)\sfill="[^"]*"/i,
        '$1 fill="currentColor"',
      )
      .replace(
        /<svg\b([^>]*)>/i,
        '<svg$1 fill="currentColor" stroke="currentColor" color="currentColor">',
      );
  }

  next = next
    .replace(/<defs>[\s\S]*?<\/defs>/gi, "")
    .replace(/\sfill="(?!none)[^"]*"/gi, ' fill="currentColor"')
    .replace(/\sstroke="(?!none)[^"]*"/gi, ' stroke="currentColor"')
    .replace(/\sstop-color="[^"]*"/gi, ' stop-color="currentColor"')
    .replace(/\sstyle="[^"]*"/gi, "")
    .replace(
      /<svg\b([^>]*)>/i,
      '<svg$1 fill="currentColor" stroke="currentColor" color="currentColor">',
    );

  return next;
}
