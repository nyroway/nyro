import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import clsx from "clsx";
import {
  BarChart3,
  Database,
  Home,
  KeyRound,
  Network,
  Plug,
  Plus,
  Route,
  ScrollText,
  Search,
  Server,
  Settings,
  Users,
} from "lucide-react";

import { buildResourceCommands, type ResourceCommand } from "@/lib/command-items";
import { consumersApi } from "@/lib/api/consumers";
import { routesApi } from "@/lib/api/routes";
import { upstreamsApi } from "@/lib/api/upstreams";
import { useLocale } from "@/lib/i18n";
import type { Consumer, Route as RouteType, Upstream } from "@/lib/types";

/* Global search (baseline new-webui #globalSearch): not a popup, but a dropdown panel expanded in
   place from the top bar search box — label.global-search-field is a real input, focus expands it,
   typing filters immediately, hits are presented by group (.search-group / .search-hit), and an empty result shows .search-empty. */

type Hit = {
  id: string;
  label: string;
  meta?: string;
  href: string;
  icon: typeof Server;
  keywords: string[];
};

type HitGroup = { key: string; label: string; hits: Hit[] };

const RESOURCE_ICON: Record<ResourceCommand["kind"], typeof Server> = {
  provider: Server,
  model: Route,
  consumer: Users,
};

const RESOURCE_PAGE: Record<ResourceCommand["kind"], "nav.providers" | "nav.models" | "nav.apiKeys"> = {
  provider: "nav.providers",
  model: "nav.models",
  consumer: "nav.apiKeys",
};

export function GlobalSearch() {
  const navigate = useNavigate();
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const rootRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const upstreams = useQuery<Upstream[]>({ queryKey: ["providers"], queryFn: () => upstreamsApi.list(), enabled: open });
  const routes = useQuery<RouteType[]>({ queryKey: ["routes"], queryFn: () => routesApi.list(), enabled: open });
  const consumers = useQuery<Consumer[]>({ queryKey: ["consumers"], queryFn: () => consumersApi.list(), enabled: open });

  const resources = buildResourceCommands(upstreams.data ?? [], routes.data ?? [], consumers.data ?? []);

  const groups = useMemo<HitGroup[]>(() => {
    const needle = query.trim().toLowerCase();
    const matches = (hit: Hit) => !needle
      || [hit.label, hit.meta ?? "", ...hit.keywords].some((value) => value.toLowerCase().includes(needle));

    const actionHits: Hit[] = [
      { id: "action:provider", label: t("command.createProvider"), meta: t("nav.providers"), href: "/providers?action=create", icon: Plus, keywords: ["create", "add"] },
      { id: "action:model", label: t("command.createModel"), meta: t("nav.models"), href: "/models?action=create", icon: Plus, keywords: ["create", "add"] },
      { id: "action:consumer", label: t("command.createConsumer"), meta: t("nav.apiKeys"), href: "/api-keys?action=create", icon: Plus, keywords: ["create", "add"] },
    ];
    const pageHits: Hit[] = [
      { label: t("nav.overview"), href: "/", icon: Home, keywords: [] },
      { label: t("nav.providers"), href: "/providers", icon: Server, keywords: [] },
      { label: t("nav.models"), href: "/models", icon: Route, keywords: [] },
      { label: t("nav.apiKeys"), href: "/api-keys", icon: KeyRound, keywords: [] },
      { label: t("nav.connect"), href: "/connect", icon: Plug, keywords: [] },
      { label: t("nav.nodes"), href: "/nodes", icon: Network, keywords: [] },
      { label: t("nav.services"), href: "/services", icon: Database, keywords: [] },
      { label: t("nav.logs"), href: "/logs", icon: ScrollText, keywords: [] },
      { label: t("nav.stats"), href: "/stats", icon: BarChart3, keywords: [] },
      { label: t("nav.settings"), href: "/settings", icon: Settings, keywords: [] },
    ].map((page) => ({ id: `page:${page.href}`, ...page }));
    const resourceHits: Hit[] = resources.map((resource) => ({
      id: resource.id,
      label: resource.label,
      meta: t(RESOURCE_PAGE[resource.kind]),
      href: resource.href,
      icon: RESOURCE_ICON[resource.kind],
      keywords: resource.keywords,
    }));

    return [
      { key: "actions", label: t("command.actions"), hits: actionHits },
      { key: "pages", label: t("command.pages"), hits: pageHits },
      { key: "providers", label: t("command.providers"), hits: resourceHits.filter((hit) => hit.id.startsWith("provider:")) },
      { key: "models", label: t("command.models"), hits: resourceHits.filter((hit) => hit.id.startsWith("model:")) },
      { key: "consumers", label: t("command.consumers"), hits: resourceHits.filter((hit) => hit.id.startsWith("consumer:")) },
    ].map((group) => ({ ...group, hits: group.hits.filter(matches) })).filter((group) => group.hits.length > 0);
  }, [query, resources, t]);

  /* Close when clicking outside the panel (the equivalent of the baseline's openGlobalSearch/closeGlobalSearch). */
  useEffect(() => {
    const onPointerDown = (event: PointerEvent) => {
      if (open && rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open]);

  /* ⌘K / Ctrl+K focuses the top bar search box. */
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        inputRef.current?.focus();
        setOpen(true);
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  const pick = (href: string) => {
    setOpen(false);
    setQuery("");
    inputRef.current?.blur();
    navigate(href);
  };

  return (
    <div
      ref={rootRef}
      className={clsx("global-search", open && "open", groups.length === 0 && "is-empty")}
    >
      <label className="global-search-field">
        <Search aria-hidden="true" />
        <input
          ref={inputRef}
          type="search"
          autoComplete="off"
          placeholder={t("shell.searchPlaceholder")}
          aria-label={t("shell.searchPlaceholder")}
          value={query}
          onFocus={() => setOpen(true)}
          onChange={(event) => { setOpen(true); setQuery(event.target.value); }}
          onKeyDown={(event) => {
            if (event.key === "Escape" && open) {
              event.preventDefault();
              setOpen(false);
              inputRef.current?.blur();
            }
          }}
        />
      </label>
      <div className="global-search-panel" role="listbox" aria-label={t("shell.searchPlaceholder")}>
        <div className="search-empty">{t("command.empty")}</div>
        {groups.map((group) => (
          <div className="search-group" key={group.key}>
            <div className="search-group-label">{group.label}</div>
            {group.hits.map((hit) => (
              <button className="search-hit" type="button" role="option" aria-selected={false} key={hit.id} onClick={() => pick(hit.href)}>
                <span className="search-hit-icon"><hit.icon aria-hidden="true" /></span>
                <span className="search-hit-copy">
                  <strong>{hit.label}</strong>
                </span>
                {hit.meta && <span className="search-hit-meta">{hit.meta}</span>}
              </button>
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}
