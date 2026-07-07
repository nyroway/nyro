/* eslint-disable react-hooks/set-state-in-effect */

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, GitBranch, Pencil, Plus, Route as RouteIcon, Trash2, ToggleRight, ToggleLeft, X } from "lucide-react";

import { backend } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type {
  CreateRoute,
  CreateRouteUpstream,
  ModelCapabilities,
  Upstream,
  Route,
  ModelBalance,
  UpdateRoute,
} from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Combobox } from "@/components/ui/combobox";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

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
  upstream_id: string;
  model: string;
  weight: number;
  priority: number;
  enabled: boolean;
};

const emptyCreate: ModelForm = {
  name: "",
  balance: "weighted",
  targets: [{ upstream_id: "", model: "", weight: 100, priority: 1, enabled: true }],
  enable_auth: false,
  enable_payload: false,
};

function FieldLabel({ children }: { children: string }) {
  return <label className="ml-1 text-xs leading-none font-normal text-slate-900">{children}</label>;
}

function balanceLabel(value: ModelBalance, isZh: boolean) {
  if (value === "priority") return isZh ? "主备分级" : "Priority";
  if (value === "cooldown") return isZh ? "故障冷却" : "Cooldown";
  if (value === "latency") return isZh ? "最低延迟" : "Latency";
  return isZh ? "加权轮询" : "Weighted";
}

function hasProviderModelsEndpoint(provider?: Upstream) {
  return Boolean(provider);
}

function withCurrentModel(options: string[], current?: string) {
  if (!current || options.includes(current)) return options;
  return [current, ...options];
}


function ToggleStatusLabel({ enabled, isZh }: { enabled: boolean; isZh: boolean }) {
  return (
    <Badge variant={enabled ? "success" : "secondary"} className="connect-label-badge">
      {enabled ? (isZh ? "已启用" : "Enabled") : (isZh ? "未启用" : "Disabled")}
    </Badge>
  );
}

type ModelToggleControlProps = {
  title: string;
  isZh: boolean;
  checked: boolean;
  disabled?: boolean;
  disabledMessage?: string;
  checkedMessage?: string;
  uncheckedMessage?: string;
  switchId?: string;
  onCheckedChange: (checked: boolean) => void;
};

function ModelToggleControl({
  title,
  isZh,
  checked,
  disabled = false,
  disabledMessage,
  checkedMessage,
  uncheckedMessage,
  switchId,
  onCheckedChange,
}: ModelToggleControlProps) {
  const message = checked ? checkedMessage : uncheckedMessage;

  return (
    <div className="space-y-2">
      <FieldLabel>{title}</FieldLabel>
      <div className="flex items-center justify-between rounded-lg border border-slate-200 bg-white px-3 py-2.5">
        {disabled ? (
          <p className="text-xs text-amber-600">{disabledMessage}</p>
        ) : (
          <div className="flex items-center gap-2">
            <ToggleStatusLabel enabled={checked} isZh={isZh} />
            {message && <span className="text-xs text-slate-600">{message}</span>}
          </div>
        )}
        <Switch
          id={switchId}
          checked={checked}
          disabled={disabled}
          onCheckedChange={onCheckedChange}
        />
      </div>
    </div>
  );
}

function ModelCapabilitySummary({ caps, isZh }: { caps: ModelCapabilities; isZh: boolean }) {
  return (
    <div className="mt-2 flex items-center gap-1.5 border-t border-slate-200 pt-2">
      <div className="flex flex-wrap items-center gap-1.5">
        {caps.tool_call && <Badge variant="success" className="connect-label-badge">{isZh ? "工具调用" : "Tools"}</Badge>}
        {caps.reasoning && <Badge variant="success" className="connect-label-badge">{isZh ? "推理" : "Reasoning"}</Badge>}
        {caps.input_modalities.includes("image") && <Badge variant="success" className="connect-label-badge">{isZh ? "视觉" : "Vision"}</Badge>}
        <Badge variant="success" className="connect-label-badge">{`ctx:${Math.round(caps.context_window / 1024)}K`}</Badge>
        {caps.embedding_length != null && caps.embedding_length > 0 && (
          <Badge variant="success" className="connect-label-badge">{`emb:${caps.embedding_length}`}</Badge>
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
  providerOptions: Array<{ value: string; label: string; provider: Upstream }>;
  providerMap: Map<string, Upstream>;
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
  const provider = providerMap.get(target.upstream_id);
  const providerHasModelDiscovery = hasProviderModelsEndpoint(provider);

  const { data: targetModels = [] } = useQuery<string[]>({
    queryKey: ["provider-models", mode, index, target.upstream_id],
    queryFn: () => backend("get_provider_models", { id: target.upstream_id }),
    enabled: !!target.upstream_id && providerHasModelDiscovery,
    staleTime: 60_000,
  });

  useEffect(() => {
    if (!target.upstream_id || !providerHasModelDiscovery) {
      setCapsQueryModel("");
      return;
    }
    const handle = window.setTimeout(() => {
      setCapsQueryModel(target.model.trim());
    }, 1000);
    return () => window.clearTimeout(handle);
  }, [target.upstream_id, target.model, providerHasModelDiscovery]);

  const { data: modelCaps } = useQuery<ModelCapabilities | null>({
    queryKey: ["model-capabilities", mode, index, target.upstream_id, capsQueryModel],
    queryFn: async () => {
      if (!target.upstream_id || !capsQueryModel.trim()) return null;
      try {
        return await backend<ModelCapabilities>("get_model_capabilities", {
          providerId: target.upstream_id,
          model: capsQueryModel.trim(),
        });
      } catch {
        return null;
      }
    },
    enabled: Boolean(target.upstream_id && capsQueryModel.trim() && providerHasModelDiscovery),
    retry: false,
    staleTime: 60_000,
  });

  const rowClassName = "grid w-full grid-cols-[minmax(0,2.6fr)_minmax(0,4.8fr)_minmax(0,1.4fr)_28px_32px] items-center gap-2.5";

  return (
    <div className="rounded-xl border border-slate-200 bg-white px-3 py-2.5">
      <div className={rowClassName}>
        <Select
          value={target.upstream_id || undefined}
          onValueChange={(value) => {
            onUpdate(index, { upstream_id: value, model: "" });
            setCapsQueryModel("");
          }}
        >
          <SelectTrigger className="bg-white">
            <SelectValue placeholder={isZh ? "选择提供商" : "Select provider"} />
          </SelectTrigger>
          <SelectContent>
            {providerOptions.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                <span className="flex items-center gap-2">
                  <ProviderIcon
                    name={option.provider.name}
                    protocol={option.provider.protocol}
                    baseUrl={option.provider.base_url}
                    size={16}
                  />
                  <span>{option.label}</span>
                </span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        {providerHasModelDiscovery ? (
          <Combobox
            value={target.model}
            className="bg-white"
            options={withCurrentModel(targetModels, target.model).map((model) => ({
              value: model,
              label: model,
            }))}
            placeholder={isZh ? "选择目标模型 ID" : "Select target model ID"}
            searchPlaceholder={isZh ? "搜索模型..." : "Search model..."}
            emptyText={isZh ? "暂无可用模型" : "No models available"}
            onValueChange={(value) => {
              onUpdate(index, { model: value });
              setCapsQueryModel(value.trim());
            }}
          />
        ) : (
          <Input
            className="bg-white"
            value={target.model}
            onChange={(e) => onUpdate(index, { model: e.target.value })}
            placeholder={isZh ? "目标模型 ID" : "Target model ID"}
          />
        )}

        {balance === "weighted" ? (
          <Input
            className="bg-white"
            type="number"
            min={0}
            value={target.weight}
            onChange={(e) => onUpdate(index, { weight: Math.max(0, Number(e.target.value || 0)) })}
            placeholder={isZh ? "权重" : "Weight"}
          />
        ) : balance === "priority" ? (
          <Input
            className="bg-white"
            type="number"
            value={target.priority}
            onChange={(e) => onUpdate(index, { priority: Number(e.target.value || 0) })}
            placeholder={isZh ? "优先级（越小越优先）" : "Priority (lower = higher)"}
          />
        ) : (
          <div className="truncate rounded-lg border border-dashed border-slate-200 bg-slate-50 px-2 py-1.5 text-xs text-slate-400">
            {balance === "latency"
              ? (isZh ? "按延迟自动排序" : "Ordered by latency automatically")
              : (isZh ? "按声明顺序，自动跳过冷却中的目标" : "Declared order, skips targets in cooldown")}
          </div>
        )}

        <button
          type="button"
          onClick={() => onUpdate(index, { enabled: !target.enabled })}
          title={target.enabled ? (isZh ? "禁用该目标" : "Disable target") : (isZh ? "启用该目标" : "Enable target")}
          className="cursor-pointer rounded-lg p-1.5 text-slate-400 transition-colors hover:bg-slate-100"
        >
          {target.enabled ? (
            <ToggleRight className="h-4 w-4 text-green-500" />
          ) : (
            <ToggleLeft className="h-4 w-4 text-slate-400" />
          )}
        </button>

        <button
          type="button"
          onClick={() => onRemove(index)}
          disabled={disableRemove}
          className="cursor-pointer rounded-lg p-1.5 text-slate-400 transition-colors hover:bg-red-50 hover:text-red-500 disabled:cursor-not-allowed disabled:opacity-40"
        >
          <Trash2 className="h-4 w-4" />
        </button>
      </div>
      {modelCaps && (
        <ModelCapabilitySummary caps={modelCaps} isZh={isZh} />
      )}
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
  const [modelToDelete, setModelToDelete] = useState<Route | null>(null);
  const [errorDialog, setErrorDialog] = useState<{ title: string; description?: string } | null>(null);

  function formatErrorMessage(error: unknown) {
    return localizeBackendErrorMessage(error, isZh);
  }

  function showErrorDialog(titleZh: string, titleEn: string, error: unknown) {
    setErrorDialog({
      title: isZh ? titleZh : titleEn,
      description: formatErrorMessage(error),
    });
  }

  const { data: routes = [], isLoading } = useQuery<Route[]>({
    queryKey: ["routes"],
    queryFn: () => backend("list_routes"),
  });
  const { data: providers = [] } = useQuery<Upstream[]>({
    queryKey: ["providers"],
    queryFn: () => backend("list_upstreams"),
  });
  const { data: globalEnablePayload } = useQuery<string | null>({
    queryKey: ["setting", "enable_payload"],
    queryFn: () => backend("get_setting", { key: "enable_payload" }),
  });
  const globalPayloadEnabled = !["false", "0", "off", "no"].includes(
    (globalEnablePayload ?? "true").trim().toLowerCase(),
  );

  const createMut = useMutation({
    mutationFn: (input: CreateRoute) => backend("create_route", { input }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["routes"] });
      setShowForm(false);
      setCreateForm(emptyCreate);
    },
    onError: (error: unknown) => {
      showErrorDialog("创建模型失败", "Failed to create model", error);
    },
  });
  const updateMut = useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateRoute }) => backend("update_route", { id, input }),
    onSuccess: () => {
      setEditError(null);
      setEditingId(null);
      setEditForm(null);
      qc.invalidateQueries({ queryKey: ["routes"] });
    },
    onError: (err: Error) => {
      setEditError(String(err));
      showErrorDialog("保存模型失败", "Failed to save model", err);
    },
  });
  const deleteMut = useMutation({
    mutationFn: (id: string) => backend("delete_route", { id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["routes"] }),
    onError: (error: unknown) => {
      showErrorDialog("删除模型失败", "Failed to delete model", error);
    },
  });

  const [modelToDisable, setModelToDisable] = useState<Route | null>(null);

  const toggleEnabledMut = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      backend("update_route", { id, input: { enabled } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["routes"] }),
    onError: (error: unknown) => {
      showErrorDialog("操作失败", "Operation failed", error);
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

  function startEdit(route: Route) {
    setEditingId(route.id);
    setEditError(null);
    const targets = route.upstreams?.length
      ? route.upstreams.map((t) => ({
          id: t.id,
          upstream_id: t.upstream_id,
          model: t.model,
          weight: t.weight ?? 100,
          priority: t.priority ?? 1,
          enabled: t.enabled ?? true,
        }))
      : [{ upstream_id: "", model: "", weight: 100, priority: 1, enabled: true }];
    setEditForm({
      id: route.id,
      name: route.model,
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
      targets: [...prev.targets, { upstream_id: "", model: "", weight: 100, priority: 1, enabled: true }],
    }));
  }

  function addEditTarget() {
    setEditForm((prev) => (prev
      ? { ...prev, targets: [...prev.targets, { upstream_id: "", model: "", weight: 100, priority: 1, enabled: true }] }
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

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-slate-900">{isZh ? "模型" : "Models"}</h1>
          <p className="mt-1 text-sm text-slate-500">
            {isZh ? "按模型名称精确匹配，自动开放所有接入协议" : "Exact match by model name, all ingress protocols enabled"}
          </p>
        </div>
        <Button
          onClick={() => {
            setEditingId(null);
            setEditForm(null);
            setShowForm((v) => !v);
          }}
          className="flex items-center gap-2"
        >
          <Plus className="h-4 w-4" />
          {isZh ? "新增模型" : "Add Model"}
        </Button>
      </div>

      {showForm && (
        <div className="glass rounded-2xl p-6 space-y-4">
          <h2 className="text-lg font-semibold text-slate-900">{isZh ? "新建模型" : "New Model"}</h2>
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <FieldLabel>{isZh ? "模型名称（虚拟模型 ID）" : "Model Name (Virtual Model ID)"}</FieldLabel>
              <Input
                value={createForm.name}
                onChange={(e) => setCreateForm((prev) => ({ ...prev, name: e.target.value }))}
                placeholder={isZh ? "输入模型名称" : "Enter model name"}
              />
            </div>
            <div className="space-y-2">
              <FieldLabel>{isZh ? "负载策略" : "Load Strategy"}</FieldLabel>
              <Select
                value={createForm.balance}
                onValueChange={(value: ModelBalance) =>
                  setCreateForm((prev) => ({ ...prev, balance: value }))
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="weighted">{isZh ? "加权轮询" : "Weighted"}</SelectItem>
                  <SelectItem value="priority">{isZh ? "主备分级" : "Priority"}</SelectItem>
                  <SelectItem value="cooldown">{isZh ? "故障冷却" : "Cooldown"}</SelectItem>
                  <SelectItem value="latency">{isZh ? "最低延迟" : "Latency"}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="col-span-2 space-y-2">
              <div className="flex items-center justify-between">
                <FieldLabel>{isZh ? "目标列表" : "Targets"}</FieldLabel>
                <span className="text-xs text-slate-500">
                  {isZh ? `共 ${createForm.targets.length} 个节点` : `${createForm.targets.length} nodes`}
                </span>
              </div>
              <div className="route-targets-panel space-y-2.5 rounded-2xl border border-slate-200/90 bg-white/80 p-3">
                {createForm.targets.map((target, index) => (
                  <TargetRow
                    key={index}
                    mode="create"
                    index={index}
                    target={target}
                    balance={createForm.balance}
                    isZh={isZh}
                    providerOptions={providerOptions}
                    providerMap={providerMap}
                    onUpdate={updateCreateTarget}
                    onRemove={removeCreateTarget}
                    disableRemove={createForm.targets.length <= 1}
                  />
                ))}
                <Button
                  type="button"
                  variant="secondary"
                  onClick={addCreateTarget}
                  className="h-10 w-full justify-center rounded-xl border border-slate-300 bg-white text-slate-700 hover:bg-slate-50"
                >
                  <Plus className="mr-2 h-4 w-4" />
                  {isZh ? "添加模型" : "Add model"}
                </Button>
              </div>
            </div>
            <ModelToggleControl
              title={isZh ? "访问控制" : "Access Control"}
              isZh={isZh}
              checked={createForm.enable_auth}
              checkedMessage={isZh ? "仅绑定该模型的 API Key 可访问" : "Only API keys bound to this model can access"}
              uncheckedMessage={isZh ? "任何请求均可访问，无需 API Key" : "Any request can access without an API key"}
              switchId="create-route-enable-auth"
              onCheckedChange={(checked) => setCreateForm((prev) => ({ ...prev, enable_auth: checked }))}
            />
            {globalPayloadEnabled && (
              <ModelToggleControl
                title={isZh ? "记录载荷" : "Record Payloads"}
                isZh={isZh}
                checked={createForm.enable_payload}
                checkedMessage={isZh ? "记录完整的请求与响应内容" : "Log full request and response content"}
                uncheckedMessage={isZh ? "仅记录 Token 用量等基础指标" : "Only basic metrics are logged"}
                switchId="create-route-enable-payload"
                onCheckedChange={(checked) => setCreateForm((prev) => ({ ...prev, enable_payload: checked }))}
              />
            )}
          </div>
          <div className="flex gap-3">
            <Button
              onClick={() =>
                createMut.mutate(buildCreatePayload(createForm))
              }
              disabled={
                createMut.isPending ||
                !createForm.name.trim() ||
                createForm.targets.some((target) => !target.upstream_id || !target.model.trim())
              }
            >
              {createMut.isPending ? (isZh ? "创建中..." : "Creating...") : (isZh ? "创建" : "Create")}
            </Button>
            <Button
              variant="secondary"
              onClick={() => {
                setShowForm(false);
                setCreateForm(emptyCreate);
              }}
            >
              {isZh ? "取消" : "Cancel"}
            </Button>
          </div>
        </div>
      )}

      {isLoading ? (
        <div className="py-12 text-center text-sm text-slate-500">{isZh ? "加载中..." : "Loading..."}</div>
      ) : routes.length === 0 ? (
        <div className="glass rounded-2xl p-12 text-center">
          <RouteIcon className="mx-auto h-10 w-10 text-slate-400" />
          <p className="mt-3 text-sm text-slate-500">{isZh ? "还没有配置模型" : "No models configured"}</p>
        </div>
      ) : (
        <div className="grid gap-3">
          {pagedRoutes.map((route) => {
            const isEditing = editingId === route.id && editForm;

            if (isEditing && editForm) {
              return (
                <div key={route.id} className="glass rounded-2xl p-5 space-y-4">
                  <div className="flex items-center justify-between">
                    <h3 className="text-sm font-semibold text-slate-900">{isZh ? "编辑模型" : "Edit Model"}</h3>
                    <button
                      onClick={() => {
                        setEditingId(null);
                        setEditForm(null);
                        setEditError(null);
                      }}
                      className="cursor-pointer p-1 text-slate-400 hover:text-slate-600"
                    >
                      <X className="h-4 w-4" />
                    </button>
                  </div>
                  <div className="grid grid-cols-2 gap-4">
                    <div className="space-y-2">
                      <FieldLabel>{isZh ? "模型名称（虚拟模型 ID）" : "Model Name (Virtual Model ID)"}</FieldLabel>
                      <Input
                        value={editForm.name}
                        onChange={(e) => setEditForm((prev) => (prev ? { ...prev, name: e.target.value } : prev))}
                      />
                    </div>
                    <div className="space-y-2">
                      <FieldLabel>{isZh ? "负载策略" : "Load Strategy"}</FieldLabel>
                      <Select
                        value={editForm.balance}
                        onValueChange={(value: ModelBalance) =>
                          setEditForm((prev) => (prev ? { ...prev, balance: value } : prev))
                        }
                      >
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="weighted">{isZh ? "加权轮询" : "Weighted"}</SelectItem>
                          <SelectItem value="priority">{isZh ? "主备分级" : "Priority"}</SelectItem>
                          <SelectItem value="cooldown">{isZh ? "故障冷却" : "Cooldown"}</SelectItem>
                          <SelectItem value="latency">{isZh ? "最低延迟" : "Latency"}</SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="col-span-2 space-y-2">
                      <div className="flex items-center justify-between">
                        <FieldLabel>{isZh ? "目标列表" : "Targets"}</FieldLabel>
                        <span className="text-xs text-slate-500">
                          {isZh ? `共 ${editForm.targets.length} 个节点` : `${editForm.targets.length} nodes`}
                        </span>
                      </div>
                      <div className="route-targets-panel space-y-2.5 rounded-2xl border border-slate-200/90 bg-white/80 p-3">
                        {editForm.targets.map((target, index) => (
                          <TargetRow
                            key={`${target.id ?? "new"}-${index}`}
                            mode="edit"
                            index={index}
                            target={target}
                            balance={editForm.balance}
                            isZh={isZh}
                            providerOptions={providerOptions}
                            providerMap={providerMap}
                            onUpdate={updateEditTarget}
                            onRemove={removeEditTarget}
                            disableRemove={editForm.targets.length <= 1}
                          />
                        ))}
                        <Button
                          type="button"
                          variant="secondary"
                          onClick={addEditTarget}
                          className="h-10 w-full justify-center rounded-xl border border-slate-300 bg-white text-slate-700 hover:bg-slate-50"
                        >
                          <Plus className="mr-2 h-4 w-4" />
                          {isZh ? "添加模型" : "Add model"}
                        </Button>
                      </div>
                    </div>
                    <ModelToggleControl
                      title={isZh ? "访问控制" : "Access Control"}
                      isZh={isZh}
                      checked={editForm.enable_auth}
                      checkedMessage={isZh ? "仅绑定该模型的 API Key 可访问" : "Only API keys bound to this model can access"}
                      uncheckedMessage={isZh ? "任何请求均可访问，无需 API Key" : "Any request can access without an API key"}
                      onCheckedChange={(checked) =>
                        setEditForm((prev) => (prev ? { ...prev, enable_auth: checked } : prev))
                      }
                    />
                    {globalPayloadEnabled && (
                      <ModelToggleControl
                        title={isZh ? "记录载荷" : "Record Payloads"}
                        isZh={isZh}
                        checked={editForm.enable_payload}
                        checkedMessage={isZh ? "记录完整的请求与响应内容" : "Log full request and response content"}
                        uncheckedMessage={isZh ? "仅记录 Token 用量等基础指标" : "Only basic metrics are logged"}
                        onCheckedChange={(checked) =>
                          setEditForm((prev) => (prev ? { ...prev, enable_payload: checked } : prev))
                        }
                      />
                    )}
                  </div>
                  <div className="flex gap-3">
                    <Button
                      onClick={() =>
                        updateMut.mutate({
                          id: editForm.id,
                          input: buildUpdatePayload(editForm),
                        })
                      }
                      disabled={updateMut.isPending}
                    >
                      {updateMut.isPending ? (isZh ? "保存中..." : "Saving...") : (isZh ? "保存" : "Save")}
                    </Button>
                    <Button
                      variant="secondary"
                      onClick={() => {
                        setEditingId(null);
                        setEditForm(null);
                        setEditError(null);
                      }}
                    >
                      {isZh ? "取消" : "Cancel"}
                    </Button>
                  </div>
                  {editError && <p className="rounded-lg bg-red-50 px-3 py-2 text-xs text-red-600">{editError}</p>}
                </div>
              );
            }

            return (
              <div key={route.id} className="glass flex items-center justify-between rounded-2xl p-4">
                <div className="flex items-center gap-3">
                  <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-slate-100">
                    <span className="inline-flex h-[30px] w-[30px] items-center justify-center rounded-xl border border-slate-300/70 bg-transparent">
                      <GitBranch className="h-3.5 w-3.5 text-slate-500" />
                    </span>
                  </div>
                  <div>
                    <div className="flex flex-wrap items-center gap-2">
                      <code className="inline-flex h-5 items-center rounded bg-slate-100 px-2 py-0.5 text-[10px] leading-none font-medium text-slate-600">{route.model}</code>
                    {route.upstreams && route.upstreams.length > 1 && (
                      <Badge variant="success" className="connect-label-badge">
                        {isZh ? `共 ${route.upstreams.length} 个目标` : `${route.upstreams.length} Targets`}
                      </Badge>
                    )}
                    <Badge
                      variant="secondary"
                      className="connect-label-badge bg-sky-50 text-sky-700"
                    >
                      {balanceLabel(route.balance ?? "weighted", isZh)}
                    </Badge>
                    {route.enable_auth && (
                      <Badge variant="success" className="connect-label-badge">
                        {isZh ? "鉴权" : "Auth"}
                      </Badge>
                    )}
                    {route.enable_payload === true && (
                      <Badge variant="success" className="connect-label-badge">
                        {isZh ? "记录载荷" : "Payloads"}
                      </Badge>
                    )}
                    {!route.enabled && (
                      <Badge variant="danger" className="connect-label-badge">
                        {isZh ? "已禁用" : "Disabled"}
                      </Badge>
                    )}
                  </div>
                  </div>
                </div>
                <div className="flex items-center gap-0.5">
                  <button
                    onClick={() => {
                      if (route.enabled) {
                        setModelToDisable(route);
                      } else {
                        toggleEnabledMut.mutate({ id: route.id, enabled: true });
                      }
                    }}
                    title={route.enabled ? (isZh ? "禁用" : "Disable") : (isZh ? "启用" : "Enable")}
                    className="cursor-pointer rounded-lg p-2 text-slate-400 transition-colors hover:bg-slate-100 hover:text-slate-600"
                  >
                    {route.enabled ? (
                      <ToggleRight className="h-4 w-4 text-green-500" />
                    ) : (
                      <ToggleLeft className="h-4 w-4 text-slate-400" />
                    )}
                  </button>
                  <button
                    onClick={() => startEdit(route)}
                    className="cursor-pointer rounded-lg p-2 text-slate-400 transition-colors hover:bg-blue-50 hover:text-blue-500"
                  >
                    <Pencil className="h-4 w-4" />
                  </button>
                  <button
                    onClick={() => setModelToDelete(route)}
                    className="cursor-pointer rounded-lg p-2 text-slate-400 transition-colors hover:bg-red-50 hover:text-red-500"
                  >
                    <Trash2 className="h-4 w-4" />
                  </button>
                </div>
              </div>
            );
          })}

          {routes.length > PAGE_SIZE && (
            <div className="flex items-center justify-between px-1 pt-1">
              <span className="text-xs text-slate-500">
                {isZh ? `第 ${page + 1} / ${totalPages} 页` : `Page ${page + 1} of ${totalPages}`}
              </span>
              <div className="flex gap-1">
                <Button
                  onClick={() => setPage(Math.max(0, page - 1))}
                  disabled={page === 0}
                  variant="outline"
                  size="icon"
                >
                  <ChevronLeft className="h-4 w-4" />
                </Button>
                <Button
                  onClick={() => setPage(Math.min(totalPages - 1, page + 1))}
                  disabled={page >= totalPages - 1}
                  variant="outline"
                  size="icon"
                >
                  <ChevronRight className="h-4 w-4" />
                </Button>
              </div>
            </div>
          )}
        </div>
      )}

      <ConfirmDialog
        open={Boolean(modelToDisable)}
        onOpenChange={(open) => {
          if (!open) setModelToDisable(null);
        }}
        title={isZh ? "确认禁用模型" : "Confirm model disable"}
        description={isZh ? "禁用后，该模型将不可用，确认禁用？" : "After disabling, the model will be unavailable. Confirm disable?"}
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={isZh ? "禁用" : "Disable"}
        onConfirm={() => {
          if (!modelToDisable) return;
          toggleEnabledMut.mutate({ id: modelToDisable.id, enabled: false });
          setModelToDisable(null);
        }}
      />
      <ConfirmDialog
        open={Boolean(modelToDelete)}
        onOpenChange={(open) => {
          if (!open) setModelToDelete(null);
        }}
        title={isZh ? "确认删除模型" : "Confirm model deletion"}
        description={
          modelToDelete
            ? (isZh
              ? `此操作不可撤销。确认删除「${modelToDelete.model}」吗？`
              : `This action cannot be undone. Delete "${modelToDelete.model}"?`)
            : undefined
        }
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={isZh ? "删除" : "Delete"}
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
        confirmText={isZh ? "我知道了" : "OK"}
        onConfirm={() => setErrorDialog(null)}
      />
    </div>
  );
}

function buildCreatePayload(form: ModelForm): CreateRoute {
  const upstreams: CreateRouteUpstream[] = form.targets.map((target) => ({
    upstream_id: target.upstream_id,
    model: target.model.trim(),
    weight: target.weight,
    priority: target.priority,
    enabled: target.enabled,
  }));
  return {
    model: form.name.trim(),
    balance: form.balance,
    upstreams,
    enable_auth: form.enable_auth,
    enable_payload: form.enable_payload,
  };
}

function buildUpdatePayload(form: ModelForm & { id: string }): UpdateRoute {
  const upstreams: CreateRouteUpstream[] = form.targets.map((target) => ({
    upstream_id: target.upstream_id,
    model: target.model.trim(),
    weight: target.weight,
    priority: target.priority,
    enabled: target.enabled,
  }));
  return {
    model: form.name.trim(),
    balance: form.balance,
    upstreams,
    enable_auth: form.enable_auth,
    enable_payload: form.enable_payload,
  };
}
