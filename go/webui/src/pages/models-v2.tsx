/* eslint-disable react-hooks/set-state-in-effect */

import { useCallback, useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, Pencil, Plus, Search, Trash2, ToggleLeft, ToggleRight } from "lucide-react";
import { useLocation, useNavigate } from "react-router-dom";

import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Combobox } from "@/components/ui/combobox";
import { Input } from "@/components/ui/input";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { ActionBar } from "@/components/v2/action-bar";
import { DataTable, type DataTableColumn } from "@/components/v2/data-table";
import { EmptyState } from "@/components/v2/empty-state";
import { FilterBar } from "@/components/v2/filter-bar";
import { FormSection } from "@/components/v2/form-section";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { ResourceEditorDialog } from "@/components/v2/resource-editor-dialog";
import { Status } from "@/components/v2/status";
import { Surface } from "@/components/v2/surface";
import { filterRoutes, type ModelFilters } from "@/features/models/model-view-model";
import { backend } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import { useLocale } from "@/lib/i18n";
import type { CreateRoute, CreateRouteUpstream, ModelBalance, Route, UpdateRoute, Upstream } from "@/lib/types";
import { localizedMessage, type MessageKey } from "@/lib/messages";

const PAGE_SIZE = 10;

type TargetDraft = {
  id?: string;
  upstream_id: string;
  model: string;
  weight: number;
  priority: number;
  enabled: boolean;
};

type ModelDraft = {
  name: string;
  balance: ModelBalance;
  targets: TargetDraft[];
  enable_auth: boolean;
  enable_payload: boolean;
};

const emptyDraft: ModelDraft = {
  name: "",
  balance: "weighted",
  targets: [{ upstream_id: "", model: "", weight: 100, priority: 1, enabled: true }],
  enable_auth: false,
  enable_payload: false,
};

function strategyLabel(balance: ModelBalance, isZh: boolean) {
  if (balance === "priority") return localizedMessage(isZh, "v2.models.priority");
  if (balance === "cooldown") return localizedMessage(isZh, "v2.models.cooldown");
  if (balance === "latency") return localizedMessage(isZh, "v2.models.lowestLatency");
  return localizedMessage(isZh, "v2.models.weighted");
}

function routeToDraft(route: Route): ModelDraft {
  return {
    name: route.model,
    balance: route.balance ?? "weighted",
    enable_auth: route.enable_auth,
    enable_payload: route.enable_payload ?? false,
    targets: route.upstreams?.length
      ? route.upstreams.map((target) => ({
          id: target.id,
          upstream_id: target.upstream_id,
          model: target.model,
          weight: target.weight ?? 100,
          priority: target.priority ?? 1,
          enabled: target.enabled ?? true,
        }))
      : [{ upstream_id: "", model: "", weight: 100, priority: 1, enabled: true }],
  };
}

function targetPayload(targets: TargetDraft[]): CreateRouteUpstream[] {
  return targets.map((target) => ({
    upstream_id: target.upstream_id,
    model: target.model.trim(),
    weight: target.weight,
    priority: target.priority,
    enabled: target.enabled,
  }));
}

function createPayload(draft: ModelDraft): CreateRoute {
  return {
    model: draft.name.trim(),
    balance: draft.balance,
    upstreams: targetPayload(draft.targets),
    enable_auth: draft.enable_auth,
    enable_payload: draft.enable_payload,
  };
}

function updatePayload(draft: ModelDraft): UpdateRoute {
  return createPayload(draft);
}

function TargetEditor({
  mode,
  index,
  target,
  balance,
  providers,
  isZh,
  canRemove,
  onChange,
  onRemove,
}: {
  mode: "create" | "edit";
  index: number;
  target: TargetDraft;
  balance: ModelBalance;
  providers: Upstream[];
  isZh: boolean;
  canRemove: boolean;
  onChange: (patch: Partial<TargetDraft>) => void;
  onRemove: () => void;
}) {
  const provider = providers.find((item) => item.id === target.upstream_id);
  const { data: models = [] } = useQuery<string[]>({
    queryKey: ["provider-models", mode, index, target.upstream_id],
    queryFn: () => backend("get_provider_models", { id: target.upstream_id }),
    enabled: Boolean(target.upstream_id),
    staleTime: 60_000,
  });
  const modelOptions = target.model && !models.includes(target.model) ? [target.model, ...models] : models;

  return (
    <article className="v2-target-editor">
      <header>
        <div>
          <strong>{localizedMessage(isZh, "models.backendTarget", { index: index + 1 })}</strong>
          <span>{provider?.name ?? (localizedMessage(isZh, "v2.models.noProviderSelected"))}</span>
        </div>
        <div className="v2-target-actions">
          <Status tone={target.enabled ? "success" : "neutral"}>{target.enabled ? (localizedMessage(isZh, "v2.models.receivingTraffic")) : (localizedMessage(isZh, "v2.providers.disabled"))}</Status>
          <button type="button" onClick={() => onChange({ enabled: !target.enabled })} title={target.enabled ? (localizedMessage(isZh, "v2.models.disableTarget")) : (localizedMessage(isZh, "v2.models.enableTarget"))}>{target.enabled ? <ToggleRight /> : <ToggleLeft />}</button>
          <button type="button" onClick={onRemove} disabled={!canRemove} title={localizedMessage(isZh, "v2.models.removeTarget")}><Trash2 /></button>
        </div>
      </header>
      <div className="v2-target-grid">
        <label>
          <span>{localizedMessage(isZh, "v2.providers.provider")}</span>
          <Select value={target.upstream_id || undefined} onValueChange={(upstream_id) => onChange({ upstream_id, model: "" })}>
            <SelectTrigger><SelectValue placeholder={localizedMessage(isZh, "v2.models.selectProvider")} /></SelectTrigger>
            <SelectContent>
              {providers.map((item) => (
                <SelectItem key={item.id} value={item.id}>
                  <span className="v2-provider-option"><ProviderIcon name={item.name} protocol={item.protocol} baseUrl={item.base_url} size={16} />{item.name}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </label>
        <label className="span-2">
          <span>{localizedMessage(isZh, "v2.models.upstreamModel")}</span>
          <Combobox
            value={target.model}
            options={modelOptions.map((model) => ({ value: model, label: model }))}
            placeholder={localizedMessage(isZh, "v2.models.selectOrEnterTargetModelId")}
            searchPlaceholder={localizedMessage(isZh, "v2.connect.searchModels")}
            emptyText={localizedMessage(isZh, "v2.models.noModelsAvailable")}
            onValueChange={(model) => onChange({ model })}
          />
        </label>
        {balance === "weighted" ? (
          <label><span>{localizedMessage(isZh, "v2.models.weight")}</span><Input type="number" min={0} value={target.weight} onChange={(event) => onChange({ weight: Math.max(0, Number(event.target.value || 0)) })} /></label>
        ) : balance === "priority" ? (
          <label><span>{localizedMessage(isZh, "v2.models.priority2")}</span><Input type="number" min={1} value={target.priority} onChange={(event) => onChange({ priority: Math.max(1, Number(event.target.value || 1)) })} /></label>
        ) : (
          <div className="v2-target-auto"><span>{localizedMessage(isZh, "v2.models.automaticOrder")}</span><strong>{strategyLabel(balance, isZh)}</strong></div>
        )}
      </div>
    </article>
  );
}

function ModelEditor({
  mode,
  draft,
  providers,
  payloadEnabled,
  isZh,
  onChange,
}: {
  mode: "create" | "edit";
  draft: ModelDraft;
  providers: Upstream[];
  payloadEnabled: boolean;
  isZh: boolean;
  onChange: (draft: ModelDraft) => void;
}) {
  const updateTarget = (index: number, patch: Partial<TargetDraft>) => {
    onChange({ ...draft, targets: draft.targets.map((target, itemIndex) => itemIndex === index ? { ...target, ...patch } : target) });
  };
  const weightTotal = draft.targets.filter((target) => target.enabled).reduce((sum, target) => sum + target.weight, 0);

  return (
    <div className="v2-model-editor">
      <FormSection title={localizedMessage(isZh, "v2.models.modelDetails")} description={localizedMessage(isZh, "v2.models.clientsUseThisModelNameInRequests")}>
        <label className="span-2 v2-field"><span>{localizedMessage(isZh, "v2.models.modelName")}</span><Input className="v2-mono" value={draft.name} onChange={(event) => onChange({ ...draft, name: event.target.value })} placeholder={localizedMessage(isZh, "v2.models.eGGpt5")} /></label>
        <label className="v2-field"><span>{localizedMessage(isZh, "v2.models.loadStrategy")}</span>
          <Select value={draft.balance} onValueChange={(balance: ModelBalance) => onChange({ ...draft, balance })}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="weighted">{localizedMessage(isZh, "v2.models.weighted")}</SelectItem>
              <SelectItem value="priority">{localizedMessage(isZh, "v2.models.priority")}</SelectItem>
              <SelectItem value="cooldown">{localizedMessage(isZh, "v2.models.cooldown")}</SelectItem>
              <SelectItem value="latency">{localizedMessage(isZh, "v2.models.lowestLatency")}</SelectItem>
            </SelectContent>
          </Select>
        </label>
      </FormSection>

      <FormSection title={localizedMessage(isZh, "v2.models.backendTargets")} description={localizedMessage(isZh, "v2.models.weightsDistributeTrafficBetweenTargetsAtTheSame")}>
        <div className="span-2 v2-target-list">
          {draft.targets.map((target, index) => (
            <TargetEditor
              key={`${target.id ?? "new"}-${index}`}
              mode={mode}
              index={index}
              target={target}
              balance={draft.balance}
              providers={providers}
              isZh={isZh}
              canRemove={draft.targets.length > 1}
              onChange={(patch) => updateTarget(index, patch)}
              onRemove={() => onChange({ ...draft, targets: draft.targets.filter((_, itemIndex) => itemIndex !== index) })}
            />
          ))}
          <Button type="button" variant="outline" className="v2-add-target" onClick={() => onChange({ ...draft, targets: [...draft.targets, { upstream_id: "", model: "", weight: 0, priority: 1, enabled: true }] })}><Plus />{localizedMessage(isZh, "v2.models.addBackendTarget")}</Button>
          {draft.balance === "weighted" && <div className={`v2-weight-total ${weightTotal === 100 ? "valid" : "invalid"}`}><span>{localizedMessage(isZh, "v2.models.enabledTargetWeight")}</span><strong>{weightTotal}%</strong></div>}
        </div>
      </FormSection>

      <FormSection title={localizedMessage(isZh, "v2.models.accessAndLogging")}>
        <div className="span-2 v2-switch-row"><div><strong>{localizedMessage(isZh, "v2.models.accessAuthentication")}</strong><span>{draft.enable_auth ? (localizedMessage(isZh, "v2.models.onlyAuthorizedApiKeysCanAccess")) : (localizedMessage(isZh, "v2.models.requestsWithoutAModelBindingCanAccess"))}</span></div><Switch checked={draft.enable_auth} onCheckedChange={(enable_auth) => onChange({ ...draft, enable_auth })} /></div>
        {payloadEnabled && <div className="span-2 v2-switch-row"><div><strong>{localizedMessage(isZh, "v2.models.recordPayloads")}</strong><span>{draft.enable_payload ? (localizedMessage(isZh, "v2.models.logFullRequestAndResponseBodies")) : (localizedMessage(isZh, "v2.models.logMetadataAndTokenUsageOnly"))}</span></div><Switch checked={draft.enable_payload} onCheckedChange={(enable_payload) => onChange({ ...draft, enable_payload })} /></div>}
      </FormSection>
    </div>
  );
}

export default function ModelsV2Page() {
  const { locale, t } = useLocale();
  const isZh = locale === "zh-CN";
  const queryClient = useQueryClient();
  const location = useLocation();
  const navigate = useNavigate();
  const [filters, setFilters] = useState<ModelFilters>({ query: "", status: "all" });
  const [page, setPage] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);
  const [createDraft, setCreateDraft] = useState<ModelDraft>(emptyDraft);
  const [editing, setEditing] = useState<Route | null>(null);
  const [editDraft, setEditDraft] = useState<ModelDraft | null>(null);
  const [pendingDisable, setPendingDisable] = useState<Route | null>(null);
  const [pendingDelete, setPendingDelete] = useState<Route | null>(null);
  const [errorDialog, setErrorDialog] = useState<{ title: string; description: string } | null>(null);

  const { data: routes = [], isLoading } = useQuery<Route[]>({ queryKey: ["routes"], queryFn: () => backend("list_routes") });
  const { data: providers = [] } = useQuery<Upstream[]>({ queryKey: ["providers"], queryFn: () => backend("list_upstreams") });
  const { data: payloadSetting } = useQuery<string | null>({ queryKey: ["setting", "enable_payload"], queryFn: () => backend("get_setting", { key: "enable_payload" }) });
  const payloadEnabled = !["false", "0", "off", "no"].includes((payloadSetting ?? "true").trim().toLowerCase());

  const showError = (titleKey: MessageKey, error: unknown) => setErrorDialog({ title: localizedMessage(isZh, titleKey), description: localizeBackendErrorMessage(error, isZh) });
  const createMutation = useMutation({
    mutationFn: (input: CreateRoute) => backend("create_route", { input }),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["routes"] }); setCreateOpen(false); setCreateDraft(emptyDraft); },
    onError: (error) => showError("models.error.create", error),
  });
  const updateMutation = useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateRoute }) => backend("update_route", { id, input }),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["routes"] }); setEditing(null); setEditDraft(null); },
    onError: (error) => showError("models.error.save", error),
  });
  const toggleMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => backend("update_route", { id, input: { enabled } }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["routes"] }),
    onError: (error) => showError("models.error.operation", error),
  });
  const deleteMutation = useMutation({
    mutationFn: (id: string) => backend("delete_route", { id }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["routes"] }),
    onError: (error) => showError("models.error.delete", error),
  });

  const startEdit = useCallback((route: Route) => { setEditing(route); setEditDraft(routeToDraft(route)); }, []);
  const filtered = useMemo(() => filterRoutes(routes, filters), [filters, routes]);
  const totalPages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const visibleRoutes = filtered.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);
  const providerMap = useMemo(() => new Map(providers.map((provider) => [provider.id, provider])), [providers]);

  useEffect(() => { setPage(0); }, [filters]);
  useEffect(() => { if (page > totalPages - 1) setPage(0); }, [page, totalPages]);
  useEffect(() => {
    const params = new URLSearchParams(location.search);
    if (params.get("action") === "create") { setCreateOpen(true); navigate(location.pathname, { replace: true }); return; }
    const focus = params.get("focus");
    const route = focus ? routes.find((item) => item.id === focus) : undefined;
    if (route) { startEdit(route); navigate(location.pathname, { replace: true }); }
  }, [location.pathname, location.search, navigate, routes, startEdit]);

  const validDraft = (draft: ModelDraft) => Boolean(draft.name.trim())
    && draft.targets.length > 0
    && draft.targets.every((target) => target.upstream_id && target.model.trim())
    && (draft.balance !== "weighted" || draft.targets.filter((target) => target.enabled).reduce((sum, target) => sum + target.weight, 0) === 100);

  const columns: DataTableColumn<Route>[] = [
    { key: "model", header: localizedMessage(isZh, "v2.connect.model"), render: (route) => <div className="v2-route-name"><code>{route.model}</code><span>{route.id}</span></div> },
    { key: "targets", header: localizedMessage(isZh, "v2.models.targets"), className: "v2-table-number", render: (route) => route.upstreams?.length ?? 0 },
    { key: "primary", header: localizedMessage(isZh, "v2.models.primaryProvider"), render: (route) => {
      const target = route.upstreams?.find((item) => item.enabled) ?? route.upstreams?.[0];
      const provider = target ? providerMap.get(target.upstream_id) : undefined;
      return target ? <div className="v2-primary-target"><strong>{provider?.name ?? target.upstream_id}</strong><code>{target.model}</code></div> : "—";
    } },
    { key: "strategy", header: localizedMessage(isZh, "v2.models.strategy"), render: (route) => <span>{strategyLabel(route.balance, isZh)}</span> },
    { key: "access", header: localizedMessage(isZh, "v2.models.access"), render: (route) => <Status tone={route.enable_auth ? "info" : "neutral"}>{route.enable_auth ? (localizedMessage(isZh, "v2.models.apiKey")) : (localizedMessage(isZh, "v2.models.open"))}</Status> },
    { key: "status", header: localizedMessage(isZh, "v2.providers.status"), render: (route) => <Status tone={route.enabled ? "success" : "neutral"}>{route.enabled ? (localizedMessage(isZh, "v2.models.running")) : (localizedMessage(isZh, "v2.providers.disabled"))}</Status> },
    { key: "actions", header: <span className="sr-only">{localizedMessage(isZh, "v2.providers.actions")}</span>, className: "v2-table-actions v2-model-actions", render: (route) => <div className="v2-row-actions">
      <button type="button" onClick={(event) => { event.stopPropagation(); if (route.enabled) setPendingDisable(route); else toggleMutation.mutate({ id: route.id, enabled: true }); }} title={route.enabled ? (localizedMessage(isZh, "v2.api-keys.disable")) : (localizedMessage(isZh, "v2.providers.enable"))}>{route.enabled ? <ToggleRight /> : <ToggleLeft />}</button>
      <button type="button" onClick={(event) => { event.stopPropagation(); startEdit(route); }} title={localizedMessage(isZh, "v2.providers.edit")}><Pencil /></button>
      <button type="button" onClick={(event) => { event.stopPropagation(); setPendingDelete(route); }} title={localizedMessage(isZh, "v2.providers.delete")}><Trash2 /></button>
    </div> },
  ];

  return (
    <PageLayout header={<PageHeader title={t("page.models.title")} description={t("page.models.subtitle")} actions={<Button onClick={() => { setCreateDraft(emptyDraft); setCreateOpen(true); }}><Plus />{localizedMessage(isZh, "v2.models.addModel")}</Button>} />}>
      <FilterBar summary={localizedMessage(isZh, "common.showing", { visible: filtered.length, total: routes.length })}>
        <label className="v2-search-field"><Search /><Input aria-label={localizedMessage(isZh, "v2.models.searchModels")} placeholder={localizedMessage(isZh, "v2.models.searchModelStrategyOrTarget")} value={filters.query} onChange={(event) => setFilters((current) => ({ ...current, query: event.target.value }))} /></label>
        <Select value={filters.status} onValueChange={(status) => setFilters((current) => ({ ...current, status: status as ModelFilters["status"] }))}>
          <SelectTrigger aria-label={localizedMessage(isZh, "v2.providers.filterByStatus")}><SelectValue /></SelectTrigger>
          <SelectContent><SelectItem value="all">{localizedMessage(isZh, "v2.providers.allStatuses")}</SelectItem><SelectItem value="enabled">{localizedMessage(isZh, "v2.models.running")}</SelectItem><SelectItem value="disabled">{localizedMessage(isZh, "v2.providers.disabled")}</SelectItem></SelectContent>
        </Select>
      </FilterBar>
      <Surface className="v2-table-surface" title={localizedMessage(isZh, "v2.models.modelRoutes")} description={localizedMessage(isZh, "v2.models.modelNamesMatchExactlyAndRouteAcrossBackend")}>
        <DataTable columns={columns} rows={visibleRoutes} rowKey={(route) => route.id} loading={isLoading} onRowClick={startEdit} empty={<EmptyState title={routes.length ? (localizedMessage(isZh, "v2.models.noMatchingModels")) : (localizedMessage(isZh, "v2.models.noModelsConfigured"))} description={routes.length ? (localizedMessage(isZh, "v2.api-keys.adjustTheSearchOrStatusFilter")) : (localizedMessage(isZh, "v2.models.createAClientFacingModelNameAndBind"))} action={!routes.length ? <Button onClick={() => setCreateOpen(true)}><Plus />{localizedMessage(isZh, "v2.models.addModel")}</Button> : undefined} />} />
        {filtered.length > PAGE_SIZE && <div className="v2-pagination"><span>{localizedMessage(isZh, "common.pagination", { page: page + 1, total: totalPages })}</span><div><Button variant="outline" size="icon" disabled={page === 0} onClick={() => setPage((current) => current - 1)}><ChevronLeft /></Button><Button variant="outline" size="icon" disabled={page >= totalPages - 1} onClick={() => setPage((current) => current + 1)}><ChevronRight /></Button></div></div>}
      </Surface>

      <ResourceEditorDialog open={createOpen} title={localizedMessage(isZh, "v2.models.addModel2")} description={localizedMessage(isZh, "v2.models.createAClientFacingModelNameAndBackend")} onClose={() => { setCreateOpen(false); setCreateDraft(emptyDraft); }} footer={<ActionBar secondary={<Button variant="outline" onClick={() => setCreateOpen(false)}>{localizedMessage(isZh, "v2.providers.cancel")}</Button>} primary={<Button disabled={!validDraft(createDraft) || createMutation.isPending} onClick={() => createMutation.mutate(createPayload(createDraft))}>{createMutation.isPending ? (localizedMessage(isZh, "v2.models.creating")) : (localizedMessage(isZh, "v2.models.createModel"))}</Button>} />}>
        <ModelEditor mode="create" draft={createDraft} providers={providers.filter((provider) => provider.enabled)} payloadEnabled={payloadEnabled} isZh={isZh} onChange={setCreateDraft} />
      </ResourceEditorDialog>
      <ResourceEditorDialog open={Boolean(editing && editDraft)} title={localizedMessage(isZh, "v2.models.editModel")} description={editing?.model} onClose={() => { setEditing(null); setEditDraft(null); }} footer={editDraft && editing ? <ActionBar secondary={<Button variant="outline" onClick={() => { setEditing(null); setEditDraft(null); }}>{localizedMessage(isZh, "v2.providers.cancel")}</Button>} primary={<Button disabled={!validDraft(editDraft) || updateMutation.isPending} onClick={() => updateMutation.mutate({ id: editing.id, input: updatePayload(editDraft) })}>{updateMutation.isPending ? (localizedMessage(isZh, "v2.models.saving")) : (localizedMessage(isZh, "v2.models.saveModel"))}</Button>} /> : undefined}>
        {editDraft && <ModelEditor mode="edit" draft={editDraft} providers={providers} payloadEnabled={payloadEnabled} isZh={isZh} onChange={setEditDraft} />}
      </ResourceEditorDialog>

      <ConfirmDialog open={Boolean(pendingDisable)} onOpenChange={(open) => { if (!open) setPendingDisable(null); }} title={localizedMessage(isZh, "v2.models.disableModel")} description={localizedMessage(isZh, "v2.models.clientsWillNoLongerBeAbleToRequest")} cancelText={localizedMessage(isZh, "v2.providers.cancel")} confirmText={localizedMessage(isZh, "v2.api-keys.disable")} onConfirm={() => { if (pendingDisable) toggleMutation.mutate({ id: pendingDisable.id, enabled: false }); setPendingDisable(null); }} />
      <ConfirmDialog open={Boolean(pendingDelete)} onOpenChange={(open) => { if (!open) setPendingDelete(null); }} title={localizedMessage(isZh, "v2.models.deleteModel")} description={pendingDelete ? localizedMessage(isZh, "models.deleteConfirm", { name: pendingDelete.model }) : undefined} cancelText={localizedMessage(isZh, "v2.providers.cancel")} confirmText={localizedMessage(isZh, "v2.providers.delete")} onConfirm={() => { if (pendingDelete) deleteMutation.mutate(pendingDelete.id); setPendingDelete(null); }} />
      <ConfirmDialog open={Boolean(errorDialog)} onOpenChange={(open) => { if (!open) setErrorDialog(null); }} title={errorDialog?.title ?? ""} description={errorDialog?.description} hideCancel confirmText={localizedMessage(isZh, "v2.providers.ok")} onConfirm={() => setErrorDialog(null)} />
    </PageLayout>
  );
}
