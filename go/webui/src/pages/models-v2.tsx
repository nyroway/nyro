/* eslint-disable react-hooks/set-state-in-effect */

import { useCallback, useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, Filter, Pencil, Search, ToggleLeft, ToggleRight, Trash2 } from "lucide-react";
import { useLocation, useNavigate } from "react-router-dom";

import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { NyroMultiCheck, NyroSearchSelect, NyroTextField } from "@/components/ui/nyro-fields";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { DataTable, type DataTableColumn } from "@/components/v2/data-table";
import { EmptyState } from "@/components/v2/empty-state";
import { FilterBar } from "@/components/v2/filter-bar";
import { FormSection } from "@/components/v2/form-section";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { ResourceEditorDrawer } from "@/components/v2/resource-editor-dialog";
import { Status } from "@/components/v2/status";
import clsx from "clsx";
import { filterRoutes, toggleTargetsProviders, type ModelFilters, type RouteTargetDraft } from "@/features/models/model-view-model";
import { routesApi } from "@/lib/api/routes";
import { settingsApi } from "@/lib/api/settings";
import { upstreamsApi } from "@/lib/api/upstreams";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import { useLocale } from "@/lib/i18n";
import type { CreateRoute, CreateRouteUpstream, ModelBalance, Route, UpdateRoute, Upstream } from "@/lib/types";
import { localizedMessage, type MessageKey } from "@/lib/messages";

const PAGE_SIZE = 10;
// Sentinel empty arrays: destructuring `data` with `= []` mints a fresh array every
// render while the query is in flight, destabilizing anything downstream that depends
// on it (see providers.tsx's React #185 note). Module constants keep it stable.
const NO_MODELS: string[] = [];
const NO_ROUTES: Route[] = [];
const NO_UPSTREAMS: Upstream[] = [];

type TargetDraft = RouteTargetDraft;

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
  targets: [],
  enable_auth: false,
  enable_payload: false,
};

const BALANCE_OPTIONS: { value: ModelBalance; labelKey: MessageKey; hintKey: MessageKey }[] = [
  { value: "weighted", labelKey: "v2.models.weighted", hintKey: "v2.models.weightedHint" },
  { value: "priority", labelKey: "v2.models.priority", hintKey: "v2.models.priorityHint" },
  { value: "cooldown", labelKey: "v2.models.cooldown", hintKey: "v2.models.cooldownHint" },
  { value: "latency", labelKey: "v2.models.lowestLatency", hintKey: "v2.models.latencyHint" },
];

function strategyLabel(balance: ModelBalance, isZh: boolean) {
  const option = BALANCE_OPTIONS.find((item) => item.value === balance);
  return localizedMessage(isZh, option?.labelKey ?? "v2.models.weighted");
}

function strategyHint(balance: ModelBalance, isZh: boolean) {
  const option = BALANCE_OPTIONS.find((item) => item.value === balance);
  return localizedMessage(isZh, option?.hintKey ?? "v2.models.weightedHint");
}

function routeToDraft(route: Route): ModelDraft {
  return {
    name: route.model,
    balance: route.balance ?? "weighted",
    enable_auth: route.enable_auth,
    enable_payload: route.enable_payload ?? false,
    targets: (route.upstreams ?? []).map((target) => ({
      id: target.id,
      upstream_id: target.upstream_id,
      model: target.model,
      weight: target.weight ?? 100,
      priority: target.priority ?? 1,
      enabled: target.enabled ?? true,
    })),
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

type ModelOption = { id: string; label: string };

function TargetEditor({
  index,
  target,
  balance,
  providers,
  isZh,
  canRemove,
  onChange,
  onRemove,
}: {
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
  const { data: models = NO_MODELS } = useQuery<string[]>({
    queryKey: ["provider-models", target.upstream_id],
    queryFn: () => upstreamsApi.models(target.upstream_id),
    enabled: Boolean(target.upstream_id),
    staleTime: 60_000,
  });
  const modelOptions = useMemo(() => models.map((model) => ({ id: model, label: model })), [models]);
  const modelValue: ModelOption | null = target.model ? { id: target.model, label: target.model } : null;

  return (
    <article className="target-editor">
      <header>
        <div className="target-editor-copy">
          <strong className="target-editor-title">{localizedMessage(isZh, "models.backendTarget", { index: index + 1 })}</strong>
          <span className="target-editor-sub">{provider?.name ?? (target.upstream_id || localizedMessage(isZh, "v2.models.noProviderSelected"))}</span>
        </div>
        <div className="target-editor-actions">
          <Status tone={target.enabled ? "success" : "neutral"}>{target.enabled ? (localizedMessage(isZh, "v2.models.receivingTraffic")) : (localizedMessage(isZh, "v2.providers.disabled"))}</Status>
          <button
            type="button"
            className={clsx("switch-control", "sm", target.enabled && "on")}
            aria-pressed={target.enabled}
            aria-label={target.enabled ? (localizedMessage(isZh, "v2.models.disableTarget")) : (localizedMessage(isZh, "v2.models.enableTarget"))}
            title={target.enabled ? (localizedMessage(isZh, "v2.models.disableTarget")) : (localizedMessage(isZh, "v2.models.enableTarget"))}
            onClick={() => onChange({ enabled: !target.enabled })}
          />
          <button
            type="button"
            className="icon-action danger"
            disabled={!canRemove}
            aria-label={localizedMessage(isZh, "v2.models.removeTarget")}
            data-tip={localizedMessage(isZh, "v2.models.removeTarget")}
            onClick={onRemove}
          >
            <Trash2 aria-hidden="true" />
          </button>
        </div>
      </header>
      <div className="target-editor-body">
        <NyroSearchSelect<ModelOption>
          label={localizedMessage(isZh, "v2.models.upstreamModel")}
          options={modelOptions}
          value={modelValue}
          onChange={(option) => onChange({ model: option?.id ?? "" })}
          getOptionLabel={(option) => option.label}
          isOptionEqualToValue={(a, b) => a.id === b.id}
          createOptionFromInput={(input) => ({ id: input, label: input })}
          searchPlaceholder={localizedMessage(isZh, "common.search")}
          placeholder={localizedMessage(isZh, "v2.models.selectOrEnterTargetModelId")}
          noOptionsText={localizedMessage(isZh, "v2.models.noModelsAvailable")}
          controlClassName="input-mono"
        />
        {balance === "weighted" ? (
          <NyroTextField
            label={localizedMessage(isZh, "v2.models.weight")}
            type="number"
            min={0}
            value={target.weight}
            onChange={(event) => onChange({ weight: Math.max(0, Number(event.target.value || 0)) })}
          />
        ) : balance === "priority" ? (
          <NyroTextField
            label={localizedMessage(isZh, "v2.models.priority2")}
            type="number"
            min={1}
            value={target.priority}
            onChange={(event) => onChange({ priority: Math.max(1, Number(event.target.value || 1)) })}
          />
        ) : (
          <dl className="descriptions">
            <dt>{localizedMessage(isZh, "v2.models.automaticOrder")}</dt>
            <dd>{strategyLabel(balance, isZh)}</dd>
          </dl>
        )}
      </div>
    </article>
  );
}

function ModelEditor({
  draft,
  providers,
  payloadEnabled,
  isZh,
  onChange,
}: {
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
  const selectedProviders = providers.filter((provider) => draft.targets.some((target) => target.upstream_id === provider.id));

  // 上游多选即目标增删（纯函数在 model-view-model，勾选追加空目标卡/取消移除）。
  const toggleProviders = (next: Upstream[]) => {
    onChange({ ...draft, targets: toggleTargetsProviders(draft.targets, next) });
  };

  return (
    <div>
      <FormSection title={localizedMessage(isZh, "v2.models.modelDetails")} description={localizedMessage(isZh, "v2.models.clientsUseThisModelNameInRequests")}>
        <NyroTextField
          label={localizedMessage(isZh, "v2.models.modelName")}
          className="input-mono"
          value={draft.name}
          onChange={(event) => onChange({ ...draft, name: event.target.value })}
          placeholder={localizedMessage(isZh, "v2.models.eGGpt5")}
        />
        <div className="field">
          <span className="field-label">{localizedMessage(isZh, "v2.models.loadStrategy")}</span>
          <div
            className="discover-switch"
            role="radiogroup"
            aria-label={localizedMessage(isZh, "v2.models.loadStrategy")}
            onKeyDown={(event) => {
              const values = BALANCE_OPTIONS.map((option) => option.value);
              const current = Math.max(0, values.indexOf(draft.balance));
              if (event.key === "ArrowRight" || event.key === "ArrowDown") {
                event.preventDefault();
                onChange({ ...draft, balance: values[(current + 1) % values.length] });
              } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
                event.preventDefault();
                onChange({ ...draft, balance: values[(current - 1 + values.length) % values.length] });
              }
            }}
          >
            {BALANCE_OPTIONS.map((option) => (
              <button
                key={option.value}
                type="button"
                role="radio"
                aria-checked={draft.balance === option.value}
                className={draft.balance === option.value ? "selected" : undefined}
                onClick={() => onChange({ ...draft, balance: option.value })}
              >
                {localizedMessage(isZh, option.labelKey)}
              </button>
            ))}
          </div>
          <span className="field-hint">{strategyHint(draft.balance, isZh)}</span>
        </div>
      </FormSection>

      <FormSection title={localizedMessage(isZh, "v2.models.backendTargets")} description={localizedMessage(isZh, "v2.models.weightsDistributeTrafficBetweenTargetsAtTheSame")}>
        <NyroMultiCheck<Upstream>
          label={localizedMessage(isZh, "v2.models.upstreams")}
          options={providers}
          selected={selectedProviders}
          onChange={toggleProviders}
          getOptionValue={(provider) => provider.id}
          getOptionLabel={(provider) => provider.name}
          renderOption={(provider) => (
            <span className="provider-option">
              <ProviderIcon name={provider.name} protocol={provider.protocol} baseUrl={provider.base_url} size={16} />
              {provider.name}
            </span>
          )}
          placeholder={localizedMessage(isZh, "v2.models.selectProvider")}
        />
        <div className="span-2 target-list">
          {draft.targets.map((target, index) => (
            <TargetEditor
              key={`${target.id ?? "new"}-${index}`}
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
        </div>
        {draft.balance === "weighted" && (
          <span className={clsx("span-2 field-hint", weightTotal !== 100 && "error")}>
            {localizedMessage(isZh, "v2.models.enabledTargetWeight")} {weightTotal}%
          </span>
        )}
      </FormSection>

      <FormSection title={localizedMessage(isZh, "v2.models.accessAndLogging")}>
        <div className="field">
          <div className="switch-row">
            <div className="switch-copy">
              <strong>{localizedMessage(isZh, "v2.models.accessAuthentication")}</strong>
              <span>{draft.enable_auth ? (localizedMessage(isZh, "v2.models.onlyAuthorizedApiKeysCanAccess")) : (localizedMessage(isZh, "v2.models.requestsWithoutAModelBindingCanAccess"))}</span>
            </div>
            <button
              type="button"
              className={clsx("switch-control", draft.enable_auth && "on")}
              aria-pressed={draft.enable_auth}
              aria-label={localizedMessage(isZh, "v2.models.accessAuthentication")}
              onClick={() => onChange({ ...draft, enable_auth: !draft.enable_auth })}
            />
          </div>
        </div>
        {payloadEnabled && (
          <div className="field">
            <div className="switch-row">
              <div className="switch-copy">
                <strong>{localizedMessage(isZh, "v2.models.recordPayloads")}</strong>
                <span>{draft.enable_payload ? (localizedMessage(isZh, "v2.models.logFullRequestAndResponseBodies")) : (localizedMessage(isZh, "v2.models.logMetadataAndTokenUsageOnly"))}</span>
              </div>
              <button
                type="button"
                className={clsx("switch-control", draft.enable_payload && "on")}
                aria-pressed={draft.enable_payload}
                aria-label={localizedMessage(isZh, "v2.models.recordPayloads")}
                onClick={() => onChange({ ...draft, enable_payload: !draft.enable_payload })}
              />
            </div>
          </div>
        )}
      </FormSection>
    </div>
  );
}

type StatusOption = { id: string; label: string };

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

  const { data: routes = NO_ROUTES, isLoading } = useQuery<Route[]>({ queryKey: ["routes"], queryFn: () => routesApi.list() });
  const { data: providers = NO_UPSTREAMS } = useQuery<Upstream[]>({ queryKey: ["providers"], queryFn: () => upstreamsApi.list() });
  const { data: payloadSetting } = useQuery<string | null>({ queryKey: ["setting", "enable_payload"], queryFn: () => settingsApi.get("enable_payload") });
  const payloadEnabled = !["false", "0", "off", "no"].includes((payloadSetting ?? "true").trim().toLowerCase());

  const showError = (titleKey: MessageKey, error: unknown) => setErrorDialog({ title: localizedMessage(isZh, titleKey), description: localizeBackendErrorMessage(error, isZh) });
  const createMutation = useMutation({
    mutationFn: (input: CreateRoute) => routesApi.create(input),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["routes"] }); setCreateOpen(false); setCreateDraft(emptyDraft); },
    onError: (error) => showError("models.error.create", error),
  });
  const updateMutation = useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateRoute }) => routesApi.update(id, input),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["routes"] }); setEditing(null); setEditDraft(null); },
    onError: (error) => showError("models.error.save", error),
  });
  const toggleMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => routesApi.update(id, { enabled }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["routes"] }),
    onError: (error) => showError("models.error.operation", error),
  });
  const deleteMutation = useMutation({
    mutationFn: (id: string) => routesApi.remove(id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["routes"] }),
    onError: (error) => showError("models.error.delete", error),
  });

  const startEdit = useCallback((route: Route) => { setEditing(route); setEditDraft(routeToDraft(route)); }, []);
  const filtered = useMemo(() => filterRoutes(routes, filters), [filters, routes]);
  const totalPages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const visibleRoutes = filtered.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);
  const providerMap = useMemo(() => new Map(providers.map((provider) => [provider.id, provider])), [providers]);

  const statusOptions = useMemo<StatusOption[]>(() => [
    { id: "all", label: localizedMessage(isZh, "v2.providers.allStatuses") },
    { id: "enabled", label: localizedMessage(isZh, "v2.models.running") },
    { id: "disabled", label: localizedMessage(isZh, "v2.providers.disabled") },
  ], [isZh]);
  const statusValue = statusOptions.find((option) => option.id === filters.status) ?? statusOptions[0];

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
    { key: "model", header: localizedMessage(isZh, "v2.connect.model"), render: (route) => (
      <div className="cell-stack">
        <code>{route.model}</code>
        <span>{route.id}</span>
      </div>
    ) },
    { key: "targets", header: localizedMessage(isZh, "v2.models.targets"), render: (route) => route.upstreams?.length ?? 0 },
    { key: "primary", header: localizedMessage(isZh, "v2.models.primaryProvider"), render: (route) => {
      const target = route.upstreams?.find((item) => item.enabled) ?? route.upstreams?.[0];
      const provider = target ? providerMap.get(target.upstream_id) : undefined;
      return target ? (
        <div className="cell-stack">
          <strong>{provider?.name ?? target.upstream_id}</strong>
          <code>{target.model}</code>
        </div>
      ) : "—";
    } },
    { key: "strategy", header: localizedMessage(isZh, "v2.models.strategy"), render: (route) => (
      <div className="cell-stack">
        <span>{strategyLabel(route.balance, isZh)}</span>
        <small>{strategyHint(route.balance, isZh)}</small>
      </div>
    ) },
    { key: "access", header: localizedMessage(isZh, "v2.models.access"), render: (route) => <Status tone={route.enable_auth ? "info" : "neutral"}>{route.enable_auth ? (localizedMessage(isZh, "v2.models.apiKey")) : (localizedMessage(isZh, "v2.models.open"))}</Status> },
    { key: "status", header: localizedMessage(isZh, "v2.providers.status"), render: (route) => <Status tone={route.enabled ? "success" : "neutral"}>{route.enabled ? (localizedMessage(isZh, "v2.models.running")) : (localizedMessage(isZh, "v2.providers.disabled"))}</Status> },
    { key: "actions", header: <span className="sr-only">{localizedMessage(isZh, "v2.providers.actions")}</span>, className: "cell-actions", render: (route) => (
      <div className="row-actions">
        <button
          type="button"
          className="icon-action"
          data-tip={route.enabled ? (localizedMessage(isZh, "v2.api-keys.disable")) : (localizedMessage(isZh, "v2.providers.enable"))}
          aria-label={route.enabled ? (localizedMessage(isZh, "v2.api-keys.disable")) : (localizedMessage(isZh, "v2.providers.enable"))}
          onClick={(event) => { event.stopPropagation(); if (route.enabled) setPendingDisable(route); else toggleMutation.mutate({ id: route.id, enabled: true }); }}
        >
          {route.enabled ? <ToggleRight aria-hidden="true" /> : <ToggleLeft aria-hidden="true" />}
        </button>
        <button
          type="button"
          className="icon-action"
          data-tip={localizedMessage(isZh, "v2.providers.edit")}
          aria-label={localizedMessage(isZh, "v2.providers.edit")}
          onClick={(event) => { event.stopPropagation(); startEdit(route); }}
        >
          <Pencil aria-hidden="true" />
        </button>
        <button
          type="button"
          className="icon-action danger"
          data-tip={localizedMessage(isZh, "v2.providers.delete")}
          aria-label={localizedMessage(isZh, "v2.providers.delete")}
          onClick={(event) => { event.stopPropagation(); setPendingDelete(route); }}
        >
          <Trash2 aria-hidden="true" />
        </button>
      </div>
    ) },
  ];

  return (
    <PageLayout header={<PageHeader title={t("page.models.title")} description={t("page.models.subtitle")} />}>
      <FilterBar>
        <label className="toolbar-search">
          <Search aria-hidden="true" />
          <input
            aria-label={localizedMessage(isZh, "v2.models.searchModels")}
            placeholder={localizedMessage(isZh, "v2.models.searchModelStrategyOrTarget")}
            value={filters.query}
            onChange={(event) => setFilters((current) => ({ ...current, query: event.target.value }))}
          />
        </label>
        <NyroSearchSelect<StatusOption>
          options={statusOptions}
          value={statusValue}
          onChange={(next) => { setFilters((current) => ({ ...current, status: (next?.id ?? "all") as ModelFilters["status"] })); }}
          getOptionLabel={(option) => option.label}
          isOptionEqualToValue={(a, b) => a.id === b.id}
          searchable={false}
          fullWidth={false}
          controlClassName="toolbar-filter"
          leadingIcon={<Filter size={14} aria-hidden="true" />}
          ariaLabel={localizedMessage(isZh, "v2.providers.filterByStatus")}
        />
        <button type="button" className="button button-primary button-sm toolbar-add" onClick={() => { setCreateDraft(emptyDraft); setCreateOpen(true); }}>
          {localizedMessage(isZh, "v2.models.addModel")}
        </button>
      </FilterBar>
      <DataTable
        carded
        columns={columns}
        rows={visibleRoutes}
        rowKey={(route) => route.id}
        loading={isLoading}
        onRowClick={startEdit}
        empty={<EmptyState
          title={routes.length ? (localizedMessage(isZh, "v2.models.noMatchingModels")) : (localizedMessage(isZh, "v2.models.noModelsConfigured"))}
          description={routes.length ? (localizedMessage(isZh, "v2.api-keys.adjustTheSearchOrStatusFilter")) : (localizedMessage(isZh, "v2.models.createAClientFacingModelNameAndBind"))}
          action={!routes.length ? (
            <button type="button" className="button button-primary" onClick={() => setCreateOpen(true)}>
              {localizedMessage(isZh, "v2.models.addModel")}
            </button>
          ) : undefined}
        />}
        footer={<>
          <span className="table-summary">{localizedMessage(isZh, "common.showing", { visible: filtered.length, total: routes.length })}</span>
          {totalPages > 1 && (
            <div className="pagination">
              <span className="table-summary">{localizedMessage(isZh, "common.pagination", { page: page + 1, total: totalPages })}</span>
              <button type="button" className="page-button" disabled={page === 0} aria-label={localizedMessage(isZh, "common.prevPage")} onClick={() => setPage((current) => current - 1)}>
                <ChevronLeft aria-hidden="true" />
              </button>
              <button type="button" className="page-button" disabled={page >= totalPages - 1} aria-label={localizedMessage(isZh, "common.nextPage")} onClick={() => setPage((current) => current + 1)}>
                <ChevronRight aria-hidden="true" />
              </button>
            </div>
          )}
        </>}
      />

      <ResourceEditorDrawer
        open={createOpen}
        title={localizedMessage(isZh, "v2.models.addModel2")}
        description={localizedMessage(isZh, "v2.models.createAClientFacingModelNameAndBackend")}
        onClose={() => { setCreateOpen(false); setCreateDraft(emptyDraft); }}
        footer={<>
          <button type="button" className="button button-secondary" onClick={() => { setCreateOpen(false); setCreateDraft(emptyDraft); }}>{localizedMessage(isZh, "v2.providers.cancel")}</button>
          <button type="button" className="button button-primary" disabled={!validDraft(createDraft) || createMutation.isPending} onClick={() => createMutation.mutate(createPayload(createDraft))}>
            {createMutation.isPending ? (localizedMessage(isZh, "v2.models.creating")) : (localizedMessage(isZh, "v2.models.createModel"))}
          </button>
        </>}
      >
        <ModelEditor draft={createDraft} providers={providers.filter((provider) => provider.enabled)} payloadEnabled={payloadEnabled} isZh={isZh} onChange={setCreateDraft} />
      </ResourceEditorDrawer>
      <ResourceEditorDrawer
        open={Boolean(editing && editDraft)}
        title={localizedMessage(isZh, "v2.models.editModel")}
        description={editing?.model}
        onClose={() => { setEditing(null); setEditDraft(null); }}
        footer={editDraft && editing ? <>
          <button type="button" className="button button-secondary" onClick={() => { setEditing(null); setEditDraft(null); }}>{localizedMessage(isZh, "v2.providers.cancel")}</button>
          <button type="button" className="button button-primary" disabled={!validDraft(editDraft) || updateMutation.isPending} onClick={() => updateMutation.mutate({ id: editing.id, input: updatePayload(editDraft) })}>
            {updateMutation.isPending ? (localizedMessage(isZh, "v2.models.saving")) : (localizedMessage(isZh, "v2.models.saveModel"))}
          </button>
        </> : undefined}
      >
        {editDraft && <ModelEditor draft={editDraft} providers={providers} payloadEnabled={payloadEnabled} isZh={isZh} onChange={setEditDraft} />}
      </ResourceEditorDrawer>

      <ConfirmDialog open={Boolean(pendingDisable)} onOpenChange={(open) => { if (!open) setPendingDisable(null); }} title={localizedMessage(isZh, "v2.models.disableModel")} description={localizedMessage(isZh, "v2.models.clientsWillNoLongerBeAbleToRequest")} cancelText={localizedMessage(isZh, "v2.providers.cancel")} confirmText={localizedMessage(isZh, "v2.api-keys.disable")} onConfirm={() => { if (pendingDisable) toggleMutation.mutate({ id: pendingDisable.id, enabled: false }); setPendingDisable(null); }} />
      <ConfirmDialog open={Boolean(pendingDelete)} onOpenChange={(open) => { if (!open) setPendingDelete(null); }} title={localizedMessage(isZh, "v2.models.deleteModel")} description={pendingDelete ? localizedMessage(isZh, "models.deleteConfirm", { name: pendingDelete.model }) : undefined} cancelText={localizedMessage(isZh, "v2.providers.cancel")} confirmText={localizedMessage(isZh, "v2.providers.delete")} onConfirm={() => { if (pendingDelete) deleteMutation.mutate(pendingDelete.id); setPendingDelete(null); }} />
      <ConfirmDialog open={Boolean(errorDialog)} onOpenChange={(open) => { if (!open) setErrorDialog(null); }} title={errorDialog?.title ?? ""} description={errorDialog?.description} hideCancel confirmText={localizedMessage(isZh, "v2.providers.ok")} onConfirm={() => setErrorDialog(null)} />
    </PageLayout>
  );
}
