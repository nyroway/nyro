import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, GitBranch, Pencil, Plus, Route as RouteIcon, Trash2 } from "lucide-react";

import { backend } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type {
  CreateModel,
  CreateModelBackend,
  ModelCapabilities,
  Provider,
  Model as ModelType,
  ModelBalance,
  UpdateModel,
  UpsertModelBackend,
} from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { CopyValue } from "@/components/ui/copy-value";
import { DetailList } from "@/components/ui/detail-list";
import { PageHeader, PageLayout } from "@/components/layout/page";
import { Drawer } from "@/components/ui/overlays";
import { NyroSelect } from "@/components/ui/nyro-select";
import { copyableTipProps } from "@/lib/copyable";
import { localizedMessage, type MessageKey } from "@/lib/messages";


const PAGE_SIZE = 7;

type ModelForm = {
  name: string;
  balance: ModelBalance;
  targets: ModelBackendForm[];
  enable_auth: boolean;
  enable_payload: boolean;
};

type ModelBackendForm = {
  id?: string;
  provider_id: string;
  model: string;
  weight: number;
  priority: number;
};

const emptyCreate: ModelForm = {
  name: "",
  balance: "weighted",
  targets: [{ provider_id: "", model: "", weight: 100, priority: 1 }],
  enable_auth: false,
  enable_payload: false,
};

function balanceLabel(value: ModelBalance, isZh: boolean) {
  if (value === "priority") return localizedMessage(isZh, "models.priority");
  return localizedMessage(isZh, "models.weighted");
}

function routeTargets(route: ModelType) {
  if (route.targets?.length) return route.targets;
  if (route.target_provider) {
    return [{ provider_id: route.target_provider, model: route.target_model, weight: 100, priority: 1 }];
  }
  return [];
}

function primaryTarget(route: ModelType) {
  const targets = routeTargets(route);
  if (!targets.length) return null;
  if (route.balance === "priority") {
    return [...targets].sort((a, b) => (a.priority ?? 1) - (b.priority ?? 1))[0];
  }
  return [...targets].sort((a, b) => (b.weight ?? 0) - (a.weight ?? 0))[0];
}

function hasProviderModelsEndpoint(provider?: Provider) {
  return Boolean(provider?.models_source?.trim());
}

function withCurrentModel(options: string[], current?: string) {
  if (!current || options.includes(current)) return options;
  return [current, ...options];
}

type ModelToggleControlProps = {
  title: string;
  checked: boolean;
  checkedMessage?: string;
  uncheckedMessage?: string;
  switchId?: string;
  onCheckedChange: (checked: boolean) => void;
};

function ModelToggleControl({
  title,
  checked,
  checkedMessage,
  uncheckedMessage,
  switchId,
  onCheckedChange,
}: ModelToggleControlProps) {
  const message = checked ? checkedMessage : uncheckedMessage;

  return (
    <div className="field">
      <span className="field-label">{title}</span>
      <div className="switch-inline">
        {message ? <span className="field-hint">{message}</span> : null}
        <button
          type="button"
          id={switchId}
          className={checked ? "switch-control on" : "switch-control"}
          aria-pressed={checked}
          aria-label={title}
          onClick={() => onCheckedChange(!checked)}
        />
      </div>
    </div>
  );
}

function ModelCapabilitySummary({ caps, isZh }: { caps: ModelCapabilities; isZh: boolean }) {
  return (
    <div className="field full">
      <div className="cell-tags">
        {caps.tool_call && <span className="tag tag-success">{localizedMessage(isZh, "common.tools")}</span>}
        {caps.reasoning && <span className="tag tag-success">{localizedMessage(isZh, "common.reasoning")}</span>}
        {caps.input_modalities.includes("image") && <span className="tag tag-success">{localizedMessage(isZh, "models.vision")}</span>}
        <span className="tag tag-success">{`ctx:${Math.round(caps.context_window / 1024)}K`}</span>
        {caps.embedding_length != null && caps.embedding_length > 0 && (
          <span className="tag tag-success">{`emb:${caps.embedding_length}`}</span>
        )}
      </div>
    </div>
  );
}

type TargetRowProps = {
  mode: "create" | "edit";
  index: number;
  target: ModelBackendForm;
  balance: ModelBalance;
  isZh: boolean;
  providerOptions: Array<{ value: string; label: string; provider: Provider }>;
  providerMap: Map<string, Provider>;
  onUpdate: (index: number, patch: Partial<ModelBackendForm>) => void;
  onRemove: (index: number) => void;
  disableRemove: boolean;
};

function TargetRow({
  mode,
  index,
  target,
  balance,
  isZh,
  providerOptions,
  providerMap,
  onUpdate,
  onRemove,
  disableRemove,
}: TargetRowProps) {
  const [capsQueryModel, setCapsQueryModel] = useState("");
  const provider = providerMap.get(target.provider_id);
  const providerHasModelDiscovery = hasProviderModelsEndpoint(provider);

  const { data: targetModels = [] } = useQuery<string[]>({
    queryKey: ["provider-models", mode, index, target.provider_id],
    queryFn: () => backend("get_provider_models", { id: target.provider_id }),
    enabled: !!target.provider_id && providerHasModelDiscovery,
    staleTime: 60_000,
  });

  useEffect(() => {
    if (!target.provider_id || !providerHasModelDiscovery) {
      setCapsQueryModel("");
      return;
    }
    const handle = window.setTimeout(() => {
      setCapsQueryModel(target.model.trim());
    }, 1000);
    return () => window.clearTimeout(handle);
  }, [target.provider_id, target.model, providerHasModelDiscovery]);

  const { data: modelCaps } = useQuery<ModelCapabilities | null>({
    queryKey: ["model-capabilities", mode, index, target.provider_id, capsQueryModel],
    queryFn: async () => {
      if (!target.provider_id || !capsQueryModel.trim()) return null;
      try {
        return await backend<ModelCapabilities>("get_model_capabilities", {
          providerId: target.provider_id,
          model: capsQueryModel.trim(),
        });
      } catch {
        return null;
      }
    },
    enabled: Boolean(target.provider_id && capsQueryModel.trim() && providerHasModelDiscovery),
    retry: false,
    staleTime: 60_000,
  });

  const modelListId = `target-models-${mode}-${index}`;

  return (
    <div className="target-editor">
      <header>
        <div className="target-editor-copy">
          <span className="target-editor-title">
            {provider ? (
              <span className="gateway-name">
                <ProviderIcon
                  name={provider.name}
                  protocol={provider.protocol}
                  baseUrl={provider.base_url}
                  size={16}
                />
                {provider.name}
              </span>
            ) : (localizedMessage(isZh, "models.selectProvider"))}
          </span>
          <span className="target-editor-sub">
            {target.model || (localizedMessage(isZh, "models.noTargetModel"))}
          </span>
        </div>
        <div className="target-editor-actions">
          <button
            type="button"
            className="icon-action danger"
            onClick={() => onRemove(index)}
            disabled={disableRemove}
            data-tip={localizedMessage(isZh, "models.removeTarget")}
            aria-label={localizedMessage(isZh, "models.removeTarget")}
          >
            <Trash2 aria-hidden="true" />
          </button>
        </div>
      </header>
      <div className="target-editor-body">
        <div className="field">
          <span className="field-label">{localizedMessage(isZh, "common.provider")}</span>
          <NyroSelect
            ariaLabel={localizedMessage(isZh, "common.provider")}
            value={target.provider_id}
            options={[
              { value: "", label: localizedMessage(isZh, "models.selectProvider") },
              ...providerOptions,
            ]}
            onChange={(value) => {
              onUpdate(index, { provider_id: value, model: "" });
              setCapsQueryModel("");
            }}
          />
        </div>

        <label className="field">
          <span className="field-label">{localizedMessage(isZh, "models.targetModel")}</span>
          {providerHasModelDiscovery ? (
            <>
              <input
                className="field-control"
                list={modelListId}
                value={target.model}
                placeholder={localizedMessage(isZh, "models.selectOrTypeTargetModelID")}
                onChange={(e) => {
                  const value = e.target.value;
                  onUpdate(index, { model: value });
                  // Selecting from the suggestion list looks up capabilities immediately; typed input still uses the 1s debounce below
                  if (value.trim() && targetModels.includes(value)) {
                    setCapsQueryModel(value.trim());
                  }
                }}
              />
              <datalist id={modelListId}>
                {withCurrentModel(targetModels, target.model).map((model) => (
                  <option key={model} value={model} />
                ))}
              </datalist>
            </>
          ) : (
            <input
              className="field-control"
              value={target.model}
              onChange={(e) => onUpdate(index, { model: e.target.value })}
              placeholder={localizedMessage(isZh, "models.targetModelID")}
            />
          )}
        </label>

        {balance === "weighted" ? (
          <label className="field">
            <span className="field-label">{localizedMessage(isZh, "models.weight")}</span>
            <input
              className="field-control"
              type="number"
              min={0}
              value={target.weight}
              onChange={(e) => onUpdate(index, { weight: Math.max(0, Number(e.target.value || 0)) })}
              placeholder={localizedMessage(isZh, "models.weight")}
            />
          </label>
        ) : (
          <div className="field">
            <span className="field-label">{localizedMessage(isZh, "models.priority2")}</span>
            <NyroSelect
              ariaLabel={localizedMessage(isZh, "models.priority2")}
              value={String(target.priority)}
              options={[
                { value: "1", label: localizedMessage(isZh, "models.primary") },
                { value: "2", label: localizedMessage(isZh, "models.fallback") },
              ]}
              onChange={(value) => onUpdate(index, { priority: Number(value) })}
            />
          </div>
        )}

        {modelCaps && (
          <ModelCapabilitySummary caps={modelCaps} isZh={isZh} />
        )}
      </div>
    </div>
  );
}

type ModelFormFieldsProps = {
  mode: "create" | "edit";
  form: ModelForm;
  isZh: boolean;
  providerOptions: Array<{ value: string; label: string; provider: Provider }>;
  providerMap: Map<string, Provider>;
  onName: (name: string) => void;
  onBalance: (balance: ModelBalance) => void;
  onUpdateTarget: (index: number, patch: Partial<ModelBackendForm>) => void;
  onRemoveTarget: (index: number) => void;
  onAddTarget: () => void;
  onAuth: (checked: boolean) => void;
  onPayload: (checked: boolean) => void;
  authSwitchId?: string;
  payloadSwitchId?: string;
};

function ModelFormFields({
  mode,
  form,
  isZh,
  providerOptions,
  providerMap,
  onName,
  onBalance,
  onUpdateTarget,
  onRemoveTarget,
  onAddTarget,
  onAuth,
  onPayload,
  authSwitchId,
  payloadSwitchId,
}: ModelFormFieldsProps) {
  return (
    <div className="form-stack">
      <div className="form-grid">
        <label className="field">
          <span className="field-label">{localizedMessage(isZh, "models.modelNameVirtualModelID")}</span>
          <input
            className="field-control"
            value={form.name}
            onChange={(e) => onName(e.target.value)}
            placeholder={mode === "create" ? (localizedMessage(isZh, "models.enterModelName")) : undefined}
          />
        </label>
        <div className="field">
          <span className="field-label">{localizedMessage(isZh, "models.loadStrategy")}</span>
          <NyroSelect
            ariaLabel={localizedMessage(isZh, "models.loadStrategy")}
            value={form.balance}
            options={[
              { value: "weighted", label: localizedMessage(isZh, "models.weighted") },
              { value: "priority", label: localizedMessage(isZh, "models.priority") },
            ]}
            onChange={(value) => onBalance(value as ModelBalance)}
          />
        </div>
        <div className="field full">
          <span className="field-label">{localizedMessage(isZh, "models.targets")}</span>
          <span className="field-hint">
            {localizedMessage(isZh, "models.nodes", { a: form.targets.length })}
          </span>
          <div className="target-list">
            {form.targets.map((target, index) => (
              <TargetRow
                key={mode === "edit" ? `${target.id ?? "new"}-${index}` : index}
                mode={mode}
                index={index}
                target={target}
                balance={form.balance}
                isZh={isZh}
                providerOptions={providerOptions}
                providerMap={providerMap}
                onUpdate={onUpdateTarget}
                onRemove={onRemoveTarget}
                disableRemove={form.targets.length <= 1}
              />
            ))}
          </div>
          <button type="button" className="button button-sm quota-add" onClick={onAddTarget}>
            <Plus />
            {localizedMessage(isZh, "models.addModel")}
          </button>
        </div>
      </div>
      <section className="form-section">
        <h3 className="form-section-title">{localizedMessage(isZh, "models.accessControl")}</h3>
        <div className="form-grid">
          <ModelToggleControl
            title={localizedMessage(isZh, "models.auth")}
            checked={form.enable_auth}
            checkedMessage={localizedMessage(isZh, "models.onlyAPIKeysBoundToThisModelCanAccess")}
            uncheckedMessage={localizedMessage(isZh, "models.anyRequestCanAccessWithoutAnAPIKey")}
            switchId={authSwitchId}
            onCheckedChange={onAuth}
          />
          <ModelToggleControl
            title={localizedMessage(isZh, "common.recordPayloads")}
            checked={form.enable_payload}
            checkedMessage={localizedMessage(isZh, "models.logFullRequestAndResponseContent")}
            uncheckedMessage={localizedMessage(isZh, "models.onlyBasicMetricsAreLogged")}
            switchId={payloadSwitchId}
            onCheckedChange={onPayload}
          />
        </div>
      </section>
    </div>
  );
}

export default function ModelsPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const qc = useQueryClient();

  const [showForm, setShowForm] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [page, setPage] = useState(0);
  const [createForm, setCreateForm] = useState<ModelForm>(emptyCreate);
  const [editForm, setEditForm] = useState<(ModelForm & { id: string }) | null>(null);
  const [editError, setEditError] = useState<string | null>(null);
  const [selectedModel, setSelectedModel] = useState<ModelType | null>(null);
  const [modelToDelete, setModelToDelete] = useState<ModelType | null>(null);
  const [errorDialog, setErrorDialog] = useState<{ title: string; description?: string } | null>(null);

  function formatErrorMessage(error: unknown) {
    return localizeBackendErrorMessage(error, isZh);
  }

  function showErrorDialog(titleKey: MessageKey, error: unknown) {
    setErrorDialog({
      title: localizedMessage(isZh, titleKey),
      description: formatErrorMessage(error),
    });
  }

  const { data: routes = [], isLoading } = useQuery<ModelType[]>({
    queryKey: ["routes"],
    queryFn: () => backend("list_models"),
  });
  const { data: providers = [] } = useQuery<Provider[]>({
    queryKey: ["providers"],
    queryFn: () => backend("get_providers"),
  });
  const createMut = useMutation({
    mutationFn: (input: CreateModel) => backend("create_model", { input }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["routes"] });
      setShowForm(false);
      setCreateForm(emptyCreate);
    },
    onError: (error: unknown) => {
      showErrorDialog("models.failedToCreateModel", error);
    },
  });
  const updateMut = useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateModel }) => backend("update_model", { id, input }),
    onSuccess: () => {
      setEditError(null);
      setEditingId(null);
      setEditForm(null);
      qc.invalidateQueries({ queryKey: ["routes"] });
    },
    onError: (err: Error) => {
      setEditError(String(err));
      showErrorDialog("models.failedToSaveModel", err);
    },
  });
  const deleteMut = useMutation({
    mutationFn: (id: string) => backend("delete_model", { id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["routes"] }),
    onError: (error: unknown) => {
      showErrorDialog("models.failedToDeleteModel", error);
    },
  });

  const [modelToDisable, setModelToDisable] = useState<ModelType | null>(null);

  const toggleEnabledMut = useMutation({
    mutationFn: ({ id, is_enabled }: { id: string; is_enabled: boolean }) =>
      backend("update_model", { id, input: { is_enabled } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["routes"] }),
    onError: (error: unknown) => {
      showErrorDialog("common.operationFailed", error);
    },
  });

  const providerOptions = useMemo(() => providers.map((p) => ({ value: p.id, label: p.name, provider: p })), [providers]);
  const providerMap = useMemo(
    () => new Map(providers.map((p) => [p.id, p])),
    [providers],
  );

  const totalPages = Math.max(1, Math.ceil(routes.length / PAGE_SIZE));
  const pagedRoutes = routes.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);

  useEffect(() => {
    if (page > totalPages - 1) setPage(0);
  }, [page, totalPages]);

  function startEdit(route: ModelType) {
    setEditingId(route.id);
    setEditError(null);
    const targets = route.targets?.length
      ? route.targets.map((t) => ({
          id: t.id,
          provider_id: t.provider_id,
          model: t.model,
          weight: t.weight ?? 100,
          priority: t.priority ?? 1,
        }))
      : [{ provider_id: route.target_provider, model: route.target_model, weight: 100, priority: 1 }];
    setEditForm({
      id: route.id,
      name: route.name,
      balance: route.balance ?? "weighted",
      targets,
      enable_auth: route.enable_auth,
      enable_payload: route.enable_payload ?? false,
    });
  }

  function updateCreateTarget(index: number, patch: Partial<ModelBackendForm>) {
    setCreateForm((prev) => ({
      ...prev,
      targets: prev.targets.map((target, idx) => (idx === index ? { ...target, ...patch } : target)),
    }));
  }

  function updateEditTarget(index: number, patch: Partial<ModelBackendForm>) {
    setEditForm((prev) => {
      if (!prev) return prev;
      return {
        ...prev,
        targets: prev.targets.map((target, idx) => (idx === index ? { ...target, ...patch } : target)),
      };
    });
  }


  function addCreateTarget() {
    setCreateForm((prev) => ({
      ...prev,
      targets: [...prev.targets, { provider_id: "", model: "", weight: 100, priority: 1 }],
    }));
  }

  function addEditTarget() {
    setEditForm((prev) => (prev
      ? { ...prev, targets: [...prev.targets, { provider_id: "", model: "", weight: 100, priority: 1 }] }
      : prev));
  }

  function removeCreateTarget(index: number) {
    setCreateForm((prev) => {
      if (prev.targets.length <= 1) return prev;
      return { ...prev, targets: prev.targets.filter((_, idx) => idx !== index) };
    });
  }

  function removeEditTarget(index: number) {
    setEditForm((prev) => {
      if (!prev || prev.targets.length <= 1) return prev;
      return { ...prev, targets: prev.targets.filter((_, idx) => idx !== index) };
    });
  }

  function closeEdit() {
    setEditingId(null);
    setEditForm(null);
    setEditError(null);
  }

  const createInvalid = !createForm.name.trim()
    || createForm.targets.some((target) => !target.provider_id || !target.model.trim());

  return (
    <PageLayout
      header={(
        <PageHeader
          title={localizedMessage(isZh, "common.models")}
          description={localizedMessage(isZh, "models.exactMatchByModelNameAllIngressProtocolsEnabled")}
          actions={(
            <button
              type="button"
              className="button button-primary"
              onClick={() => {
                setEditingId(null);
                setEditForm(null);
                setShowForm((v) => !v);
              }}
            >
              <Plus />
              {localizedMessage(isZh, "models.addModel2")}
            </button>
          )}
        />
      )}
    >
      {/* List card: paginated table. Create and edit use the right-side drawer; confirmation uses a centered modal. */}
      <div className="card table-card">
        <div className="card-header">
          <h2 className="card-title">{localizedMessage(isZh, "models.modelList")}</h2>
        </div>
        <div className="table-toolbar">
          <span className="table-summary">
            {localizedMessage(isZh, "models.models", { a: routes.length })}
          </span>
        </div>
        {isLoading ? (
          <div className="card-body">
            <div className="empty"><strong>{localizedMessage(isZh, "common.loading")}</strong></div>
          </div>
        ) : routes.length === 0 ? (
          <div className="card-body">
            <div className="empty">
              <RouteIcon size={40} />
              <strong>{localizedMessage(isZh, "models.noModelsConfigured")}</strong>
            </div>
          </div>
        ) : (
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>{localizedMessage(isZh, "common.model")}</th>
                  <th>{localizedMessage(isZh, "models.primaryProvider")}</th>
                  <th>{localizedMessage(isZh, "models.strategy")}</th>
                  <th>{localizedMessage(isZh, "models.auth")}</th>
                  <th>{localizedMessage(isZh, "common.status")}</th>
                  <th className="cell-switch">{localizedMessage(isZh, "common.enabled")}</th>
                  <th className="cell-actions">{localizedMessage(isZh, "common.actions")}</th>
                </tr>
              </thead>
              <tbody>
                {pagedRoutes.map((route) => {
                  const targets = routeTargets(route);
                  const primary = primaryTarget(route);
                  const primaryProvider = primary ? providerMap.get(primary.provider_id) : undefined;
                  return (
                  <tr
                    key={route.id}
                    className="clickable"
                    onClick={() => {
                      setShowForm(false);
                      setEditingId(null);
                      setEditForm(null);
                      setSelectedModel(route);
                    }}
                  >
                    <td>
                      <div className="gateway-name">
                        <span className="gateway-token"><GitBranch /></span>
                        <strong className="name-ellipsis" {...copyableTipProps(route.name)}>{route.name}</strong>
                      </div>
                    </td>
                    <td>
                      {primary ? (
                        <div className="cell-stack">
                          <strong className="name-ellipsis" {...copyableTipProps(primaryProvider?.name ?? primary.provider_id)}>
                            {primaryProvider?.name ?? primary.provider_id}
                          </strong>
                          <span className="name-ellipsis" {...copyableTipProps(primary.model)}>{primary.model}</span>
                          {targets.length > 1 ? (
                            <span className="cell-note">{localizedMessage(isZh, "models.targets2", { a: targets.length })}</span>
                          ) : null}
                        </div>
                      ) : "—"}
                    </td>
                    <td>{balanceLabel(route.balance ?? "weighted", isZh)}</td>
                    <td>
                      <span className={route.enable_auth ? "health" : "health is-idle"}>
                        <span className="mini-dot" />
                        {route.enable_auth ? (localizedMessage(isZh, "models.auth")) : (localizedMessage(isZh, "models.open"))}
                      </span>
                    </td>
                    <td>
                      <span className={route.enable_payload === false ? "health is-idle" : "health"}>
                        <span className="mini-dot" />
                        {route.enable_payload === false
                          ? (localizedMessage(isZh, "models.noPayloads"))
                          : (localizedMessage(isZh, "models.recording"))}
                      </span>
                    </td>
                    <td className="cell-switch">
                      <button
                        type="button"
                        role="switch"
                        className={route.is_enabled ? "switch-control sm on" : "switch-control sm"}
                        aria-checked={route.is_enabled}
                        data-tip={route.is_enabled ? (localizedMessage(isZh, "common.disable")) : (localizedMessage(isZh, "common.enable"))}
                        aria-label={route.is_enabled ? (localizedMessage(isZh, "common.disable")) : (localizedMessage(isZh, "common.enable"))}
                        onClick={(event) => {
                          event.stopPropagation();
                          if (route.is_enabled) {
                            setModelToDisable(route);
                          } else {
                            toggleEnabledMut.mutate({ id: route.id, is_enabled: true });
                          }
                        }}
                      />
                    </td>
                    <td className="cell-actions">
                      <div className="row-actions">
                        <button
                          type="button"
                          className="icon-action"
                          data-tip={localizedMessage(isZh, "common.edit")}
                          aria-label={localizedMessage(isZh, "common.edit")}
                          onClick={(event) => {
                            event.stopPropagation();
                            setSelectedModel(null);
                            startEdit(route);
                          }}
                        >
                          <Pencil aria-hidden="true" />
                        </button>
                        <button
                          type="button"
                          className="icon-action danger"
                          data-tip={localizedMessage(isZh, "common.delete")}
                          aria-label={localizedMessage(isZh, "common.delete")}
                          onClick={(event) => {
                            event.stopPropagation();
                            setModelToDelete(route);
                          }}
                        >
                          <Trash2 aria-hidden="true" />
                        </button>
                      </div>
                    </td>
                  </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        {routes.length > PAGE_SIZE && (
          <footer className="table-footer">
            <span className="table-summary">
              {localizedMessage(isZh, "common.pageOf", { a: page + 1, b: totalPages })}
            </span>
            <div className="pager">
              <button
                type="button"
                className="button button-sm"
                onClick={() => setPage(Math.max(0, page - 1))}
                disabled={page === 0}
                data-tip={localizedMessage(isZh, "common.previousPage")}
                aria-label={localizedMessage(isZh, "common.previousPage")}
              >
                <ChevronLeft aria-hidden="true" />
              </button>
              <button
                type="button"
                className="button button-sm"
                onClick={() => setPage(Math.min(totalPages - 1, page + 1))}
                disabled={page >= totalPages - 1}
                data-tip={localizedMessage(isZh, "common.nextPage")}
                aria-label={localizedMessage(isZh, "common.nextPage")}
              >
                <ChevronRight aria-hidden="true" />
              </button>
            </div>
          </footer>
        )}
      </div>

      <Drawer
        open={Boolean(selectedModel)}
        title={selectedModel?.name ?? localizedMessage(isZh, "models.modelDetails")}
        description={selectedModel?.id}
        onClose={() => setSelectedModel(null)}
        footerStart={selectedModel ? (
          <button
            type="button"
            className="button button-secondary button-danger-text"
            onClick={() => {
              const route = selectedModel;
              setSelectedModel(null);
              setModelToDelete(route);
            }}
          >
            {localizedMessage(isZh, "common.delete")}
          </button>
        ) : undefined}
        footer={selectedModel ? (
          <>
            <button type="button" className="button button-secondary" onClick={() => setSelectedModel(null)}>
              {localizedMessage(isZh, "common.close")}
            </button>
            <button
              type="button"
              className="button button-primary"
              onClick={() => {
                const route = selectedModel;
                setSelectedModel(null);
                startEdit(route);
              }}
            >
              {localizedMessage(isZh, "common.edit")}
            </button>
          </>
        ) : undefined}
      >
        {selectedModel ? (
          <DetailList
            items={[
              { label: localizedMessage(isZh, "common.name"), value: <CopyValue value={selectedModel.name} isZh={isZh} /> },
              { label: localizedMessage(isZh, "models.strategy"), value: balanceLabel(selectedModel.balance ?? "weighted", isZh) },
              { label: localizedMessage(isZh, "models.targets"), value: (
                <div className="cell-tags">
                  {(selectedModel.targets?.length
                    ? selectedModel.targets
                    : [{ provider_id: selectedModel.target_provider, model: selectedModel.target_model }]
                  ).map((target, index) => {
                    const label = `${providerMap.get(target.provider_id)?.name ?? target.provider_id} / ${target.model}`;
                    return <span key={`${target.provider_id}-${target.model}-${index}`} className="tag" {...copyableTipProps(label)}>{label}</span>;
                  })}
                </div>
              ) },
              { label: localizedMessage(isZh, "models.auth"), value: selectedModel.enable_auth ? localizedMessage(isZh, "models.auth") : localizedMessage(isZh, "models.open") },
              { label: localizedMessage(isZh, "common.recordPayloads"), value: selectedModel.enable_payload === false ? localizedMessage(isZh, "models.noPayloads") : localizedMessage(isZh, "models.recording") },
              { label: localizedMessage(isZh, "common.enabled"), value: selectedModel.is_enabled ? localizedMessage(isZh, "common.enabled2") : localizedMessage(isZh, "common.disabled") },
              { label: localizedMessage(isZh, "common.createdAt"), value: selectedModel.created_at },
            ]}
          />
        ) : null}
      </Drawer>

      <Drawer
        open={showForm}
        wide
        title={localizedMessage(isZh, "models.newModel")}
        description={localizedMessage(isZh, "models.exactMatchByModelNameAllIngressProtocolsEnabled")}
        onClose={() => setShowForm(false)}
        footer={(
          <>
            <button
              type="button"
              className="button button-secondary"
              onClick={() => {
                setShowForm(false);
                setCreateForm(emptyCreate);
              }}
            >
              {localizedMessage(isZh, "common.cancel")}
            </button>
            <button
              type="button"
              className="button button-primary"
              onClick={() => createMut.mutate(buildCreatePayload(createForm))}
              disabled={createMut.isPending || createInvalid}
            >
              {createMut.isPending ? (localizedMessage(isZh, "common.creating")) : (localizedMessage(isZh, "common.create"))}
            </button>
          </>
        )}
      >
        <ModelFormFields
          mode="create"
          form={createForm}
          isZh={isZh}
          providerOptions={providerOptions}
          providerMap={providerMap}
          onName={(name) => setCreateForm((prev) => ({ ...prev, name }))}
          onBalance={(balance) => setCreateForm((prev) => ({ ...prev, balance }))}
          onUpdateTarget={updateCreateTarget}
          onRemoveTarget={removeCreateTarget}
          onAddTarget={addCreateTarget}
          onAuth={(checked) => setCreateForm((prev) => ({ ...prev, enable_auth: checked }))}
          onPayload={(checked) => setCreateForm((prev) => ({ ...prev, enable_payload: checked }))}
          authSwitchId="create-route-enable-auth"
          payloadSwitchId="create-route-enable-payload"
        />
      </Drawer>

      <Drawer
        open={Boolean(editingId && editForm)}
        wide
        title={localizedMessage(isZh, "models.editModel")}
        description={editForm?.name}
        onClose={closeEdit}
        footer={editForm ? (
          <>
            <button type="button" className="button button-secondary" onClick={closeEdit}>
              {localizedMessage(isZh, "common.cancel")}
            </button>
            <button
              type="button"
              className="button button-primary"
              onClick={() => updateMut.mutate({
                id: editForm.id,
                input: buildUpdatePayload(editForm),
              })}
              disabled={updateMut.isPending}
            >
              {updateMut.isPending ? (localizedMessage(isZh, "common.saving")) : (localizedMessage(isZh, "common.save"))}
            </button>
          </>
        ) : undefined}
      >
        {editForm && (
          <div className="form-stack">
            <ModelFormFields
              mode="edit"
              form={editForm}
              isZh={isZh}
              providerOptions={providerOptions}
              providerMap={providerMap}
              onName={(name) => setEditForm((prev) => (prev ? { ...prev, name } : prev))}
              onBalance={(balance) => setEditForm((prev) => (prev ? { ...prev, balance } : prev))}
              onUpdateTarget={updateEditTarget}
              onRemoveTarget={removeEditTarget}
              onAddTarget={addEditTarget}
              onAuth={(checked) => setEditForm((prev) => (prev ? { ...prev, enable_auth: checked } : prev))}
              onPayload={(checked) => setEditForm((prev) => (prev ? { ...prev, enable_payload: checked } : prev))}
            />
            {editError && <p className="field-hint error">{editError}</p>}
          </div>
        )}
      </Drawer>

      <ConfirmDialog
        open={Boolean(modelToDisable)}
        onOpenChange={(open) => {
          if (!open) setModelToDisable(null);
        }}
        title={localizedMessage(isZh, "models.confirmModelDisable")}
        description={localizedMessage(isZh, "models.afterDisablingTheModelWillBeUnavailableConfirmDisable")}
        cancelText={localizedMessage(isZh, "common.cancel")}
        confirmText={localizedMessage(isZh, "common.disable")}
        onConfirm={() => {
          if (!modelToDisable) return;
          toggleEnabledMut.mutate({ id: modelToDisable.id, is_enabled: false });
          setModelToDisable(null);
        }}
      />
      <ConfirmDialog
        open={Boolean(modelToDelete)}
        onOpenChange={(open) => {
          if (!open) setModelToDelete(null);
        }}
        title={localizedMessage(isZh, "models.confirmModelDeletion")}
        description={
          modelToDelete
            ? (localizedMessage(isZh, "common.thisActionCannotBeUndoneDelete", { a: modelToDelete.name }))
            : undefined
        }
        cancelText={localizedMessage(isZh, "common.cancel")}
        confirmText={localizedMessage(isZh, "common.delete")}
        onConfirm={() => {
          if (!modelToDelete) return;
          deleteMut.mutate(modelToDelete.id);
          setModelToDelete(null);
        }}
      />
      <ConfirmDialog
        open={Boolean(errorDialog)}
        onOpenChange={(open) => {
          if (!open) setErrorDialog(null);
        }}
        title={errorDialog?.title ?? ""}
        description={errorDialog?.description}
        hideCancel
        confirmText={localizedMessage(isZh, "common.ok")}
        onConfirm={() => setErrorDialog(null)}
      />
    </PageLayout>
  );
}

function buildCreatePayload(form: ModelForm): CreateModel {
  const targets: CreateModelBackend[] = form.targets.map((target) => ({
    provider_id: target.provider_id,
    model: target.model.trim(),
    weight: target.weight,
    priority: target.priority,
  }));
  const primary = targets[0] ?? { provider_id: "", model: "" };
  return {
    name: form.name.trim(),
    balance: form.balance,
    targets,
    target_provider: primary.provider_id,
    target_model: primary.model,
    enable_auth: form.enable_auth,
    enable_payload: form.enable_payload,
  };
}

function buildUpdatePayload(form: ModelForm & { id: string }): UpdateModel {
  const targets: UpsertModelBackend[] = form.targets.map((target) => ({
    id: target.id,
    provider_id: target.provider_id,
    model: target.model.trim(),
    weight: target.weight,
    priority: target.priority,
  }));
  const primary = targets[0] ?? { provider_id: "", model: "" };
  return {
    name: form.name.trim(),
    balance: form.balance,
    targets,
    target_provider: primary.provider_id,
    target_model: primary.model,
    enable_auth: form.enable_auth,
    enable_payload: form.enable_payload,
  };
}
