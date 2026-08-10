import { useEffect } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { BarChart3, Database, Home, KeyRound, Network, Plug, Plus, Route, ScrollText, Server, Settings } from "lucide-react";

import { backend } from "@/lib/backend";
import { buildResourceCommands } from "@/lib/command-items";
import { useLocale } from "@/lib/i18n";
import type { Consumer, Route as RouteType, Upstream } from "@/lib/types";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const navigate = useNavigate();
  const { t } = useLocale();
  const upstreams = useQuery<Upstream[]>({ queryKey: ["providers"], queryFn: () => backend("list_upstreams"), enabled: open });
  const routes = useQuery<RouteType[]>({ queryKey: ["routes"], queryFn: () => backend("list_routes"), enabled: open });
  const consumers = useQuery<Consumer[]>({ queryKey: ["consumers"], queryFn: () => backend("list_consumers"), enabled: open });

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        onOpenChange(!open);
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [onOpenChange, open]);

  const go = (href: string) => {
    onOpenChange(false);
    navigate(href);
  };
  const resources = buildResourceCommands(upstreams.data ?? [], routes.data ?? [], consumers.data ?? []);
  const pages = [
    { label: t("nav.overview"), href: "/", icon: Home },
    { label: t("nav.providers"), href: "/providers", icon: Server },
    { label: t("nav.models"), href: "/models", icon: Route },
    { label: t("nav.apiKeys"), href: "/api-keys", icon: KeyRound },
    { label: t("nav.connect"), href: "/connect", icon: Plug },
    { label: t("nav.nodes"), href: "/nodes", icon: Network },
    { label: t("nav.services"), href: "/services", icon: Database },
    { label: t("nav.logs"), href: "/logs", icon: ScrollText },
    { label: t("nav.stats"), href: "/stats", icon: BarChart3 },
    { label: t("nav.settings"), href: "/settings", icon: Settings },
  ];

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="v2-command-dialog" showCloseButton={false}>
        <DialogHeader className="sr-only">
          <DialogTitle>{t("command.title")}</DialogTitle>
          <DialogDescription>{t("command.description")}</DialogDescription>
        </DialogHeader>
        <Command>
          <CommandInput autoFocus placeholder={t("shell.searchPlaceholder")} />
          <CommandList className="max-h-[420px]">
            <CommandEmpty>{t("command.empty")}</CommandEmpty>
            <CommandGroup heading={t("command.actions")}>
              {[
                [t("command.createProvider"), "/providers?action=create"],
                [t("command.createModel"), "/models?action=create"],
                [t("command.createConsumer"), "/api-keys?action=create"],
              ].map(([label, href]) => (
                <CommandItem key={href} value={`${label} ${href}`} onSelect={() => go(href)}>
                  <Plus aria-hidden="true" />{label}
                </CommandItem>
              ))}
            </CommandGroup>
            <CommandGroup heading={t("command.pages")}>
              {pages.map(({ label, href, icon: Icon }) => (
                <CommandItem key={href} value={`${label} ${href}`} onSelect={() => go(href)}>
                  <Icon aria-hidden="true" />{label}
                </CommandItem>
              ))}
            </CommandGroup>
            {(["provider", "model", "consumer"] as const).map((kind) => {
              const items = resources.filter((item) => item.kind === kind);
              if (!items.length) return null;
              const heading = kind === "provider" ? t("command.providers") : kind === "model" ? t("command.models") : t("command.consumers");
              return (
                <CommandGroup key={kind} heading={heading}>
                  {items.map((item) => (
                    <CommandItem key={item.id} value={[item.label, ...item.keywords].join(" ")} onSelect={() => go(item.href)}>
                      {item.label}
                    </CommandItem>
                  ))}
                </CommandGroup>
              );
            })}
          </CommandList>
        </Command>
      </DialogContent>
    </Dialog>
  );
}
