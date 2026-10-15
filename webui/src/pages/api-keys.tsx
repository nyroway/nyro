import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronLeft, ChevronRight, Copy, KeyRound, Pencil, Plus, Trash2, ToggleRight, ToggleLeft, X } from "lucide-react";

import { backend } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type { ApiKey, CreateApiKey, Model as ModelType, UpdateApiKey } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { MultiSelect } from "@/components/ui/multi-select";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const PAGE_SIZE = 7;

type ExpirePreset = "never" | "1d" | "7d" | "30d" | "90d" | "180d" | "1y";

const expirePresetOptions: { value: ExpirePreset; zh: string; en: string }[] = [
  { value: "never", zh: "永不过期", en: "Never" },
  { value: "1d", zh: "1 天", en: "1 day" },
  { value: "7d", zh: "7 天", en: "7 days" },
  { value: "30d", zh: "30 天", en: "30 days" },
  { value: "90d", zh: "90 天", en: "90 days" },
  { value: "180d", zh: "180 天", en: "180 days" },
  { value: "1y", zh: "1 年", en: "1 year" },
];

function FieldLabel({ children }: { children: string }) {
  return <label className="ml-1 text-xs leading-none font-normal text-slate-900">{children}</label>;
}

function shortApiKeyTag(key: string) {
  const trimmed = key.trim();
  if (!trimmed) return "sk-";
  return `${trimmed.slice(0, 8)}…`;
}

function quotaText(value: number | null | undefined) {
  return value && value > 0 ? String(value) : "∞";
}

function formatExpiresText(value: string | null | undefined, isZh: boolean) {
  if (!value) return isZh ? "永不过期" : "Never";
  return value.replace("T", " ").slice(0, 19);
}

function isApiKeyExpired(expiresAt: string | null | undefined) {
  if (!expiresAt) return false;
  const normalized = expiresAt.includes("T") ? expiresAt : expiresAt.replace(" ", "T");
  const utcMillis = Date.parse(normalized.endsWith("Z") ? normalized : `${normalized}Z`);
  if (!Number.isNaN(utcMillis)) {
    return utcMillis <= Date.now();
  }
  const fallbackMillis = Date.parse(expiresAt);
  return !Number.isNaN(fallbackMillis) && fallbackMillis <= Date.now();
}

function formatValidityLabel(expired: boolean, isZh: boolean) {
  return expired ? (isZh ? "过期" : "Expired") : (isZh ? "有效" : "Valid");
}

function resolveExpiresAt(preset: ExpirePreset) {
  if (preset === "never") return undefined;
  const now = Date.now();
  const day = 24 * 60 * 60 * 1000;
  const map: Record<Exclude<ExpirePreset, "never">, number> = {
    "1d": 1,
    "7d": 7,
    "30d": 30,
    "90d": 90,
    "180d": 180,
    "1y": 365,
  };
  const date = new Date(now + map[preset] * day);
  return date.toISOString().slice(0, 19).replace("T", " ");
}

function digitsOnly(value: string) {
  return value.replace(/[^\d]/g, "");
}

type CreateForm = {
  name: string;
  rpm: string;
  rpd: string;
  tpm: string;
  tpd: string;
  expiresPreset: ExpirePreset;
  model_ids: string[];
};

type EditForm = {
  id: string;
  key: string;
  name: string;
  expires_text: string;
  rpm: string;
  rpd: string;
  tpm: string;
  tpd: string;
  model_ids: string[];
};

const emptyCreate: CreateForm = {
  name: "",
  rpm: "",
  rpd: "",
  tpm: "",
  tpd: "",
  expiresPreset: "30d",
  model_ids: [],
};

export default function ApiKeysPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const qc = useQueryClient();

  const [showForm, setShowForm] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [createForm, setCreateForm] = useState<CreateForm>(emptyCreate);
  const [editForm, setEditForm] = useState<EditForm | null>(null);
  const [page, setPage] = useState(0);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [copiedEditKey, setCopiedEditKey] = useState(false);
  const [createdKey, setCreatedKey] = useState<string | null>(null);
  const [showCreatedDialog, setShowCreatedDialog] = useState(false);
  const [copiedCreatedKey, setCopiedCreatedKey] = useState(false);
  const [apiKeyToDelete, setApiKeyToDelete] = useState<ApiKey | null>(null);
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

  const { data: apiKeys = [], isLoading } = useQuery<ApiKey[]>({
    queryKey: ["api-keys"],
    queryFn: () => backend("list_api_keys"),
  });
  const { data: routes = [] } = useQuery<ModelType[]>({
    queryKey: ["routes"],
    queryFn: () => backend("list_models"),
  });

  const createMut = useMutation({
    mutationFn: (input: CreateApiKey) => backend<ApiKey>("create_api_key", { input }),
    onSuccess: (created) => {
      qc.invalidateQueries({ queryKey: ["api-keys"] });
      setShowForm(false);
      setCreateForm(emptyCreate);
      setCreatedKey(created.key);
      setShowCreatedDialog(true);
    },
    onError: (error: unknown) => {
      showErrorDialog("创建 API Key 失败", "Failed to create API key", error);
    },
  });
  const updateMut = useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateApiKey }) => backend("update_api_key", { id, input }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["api-keys"] });
      setEditingId(null);
      setEditForm(null);
    },
    onError: (error: unknown) => {
      showErrorDialog("保存 API Key 失败", "Failed to save API key", error);
    },
  });
  const deleteMut = useMutation({
    mutationFn: (id: string) => backend("delete_api_key", { id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["api-keys"] }),
    onError: (error: unknown) => {
      showErrorDialog("删除 API Key 失败", "Failed to delete API key", error);
    },
  });

  const toggleEnabledMut = useMutation({
    mutationFn: ({ id, is_enabled }: { id: string; is_enabled: boolean }) =>
      backend("update_api_key", { id, input: { is_enabled } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["api-keys"] }),
    onError: (error: unknown) => {
      showErrorDialog("操作失败", "Operation failed", error);
    },
  });

  const totalPages = Math.max(1, Math.ceil(apiKeys.length / PAGE_SIZE));
  const pagedApiKeys = apiKeys.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);

  useEffect(() => {
    if (page > totalPages - 1) setPage(0);
  }, [page, totalPages]);

  const routeOptions = useMemo(
    () =>
      routes.map((route) => ({
        value: route.id,
        label: route.name,
      })),
    [routes],
  );

  function startEdit(item: ApiKey) {
    setEditingId(item.id);
    setCopiedEditKey(false);
    setEditForm({
      id: item.id,
      key: item.key,
      name: item.name,
      expires_text: formatExpiresText(item.expires_at, isZh),
      rpm: item.rpm ? String(item.rpm) : "",
      rpd: item.rpd ? String(item.rpd) : "",
      tpm: item.tpm ? String(item.tpm) : "",
      tpd: item.tpd ? String(item.tpd) : "",
      model_ids: item.model_ids ?? [],
    });
  }

  async function copyKey(item: ApiKey) {
    await navigator.clipboard.writeText(item.key);
    setCopiedId(item.id);
    setTimeout(() => setCopiedId(null), 1500);
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-slate-900">{isZh ? "API Key" : "API Keys"}</h1>
          <p className="mt-1 text-sm text-slate-500">
            {isZh ? "管理鉴权 Key、配额与模型绑定" : "Manage authentication keys, quotas, and model bindings"}
          </p>
        </div>
        <Button
          onClick={() => {
            setEditingId(null);
            setEditForm(null);
            setCopiedEditKey(false);
            setShowForm((v) => !v);
          }}
          className="flex items-center gap-2"
        >
          <Plus className="h-4 w-4" />
          {isZh ? "新增 Key" : "Add Key"}
        </Button>
      </div>

      {showForm && (
        <div className="glass rounded-2xl p-6 space-y-4">
          <h2 className="text-lg font-semibold text-slate-900">{isZh ? "创建 API Key" : "Create API Key"}</h2>
          <div className="space-y-5">
            <div className="space-y-3">
              <p className="text-sm font-semibold text-slate-700">{isZh ? "1. 基本信息" : "1. Basic Information"}</p>
              <p className="text-xs text-slate-500">
                {isZh ? "Key 值会在创建后自动生成。" : "Key value is auto-generated after creation."}
              </p>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <FieldLabel>{isZh ? "名称" : "Name"}</FieldLabel>
                  <Input
                    value={createForm.name}
                    onChange={(e) => setCreateForm((prev) => ({ ...prev, name: e.target.value }))}
                    placeholder={isZh ? "例如 Frontend App" : "e.g. Frontend App"}
                  />
                </div>
                <div className="space-y-2">
                  <FieldLabel>{isZh ? "有效期" : "Validity"}</FieldLabel>
                  <Select
                    value={createForm.expiresPreset}
                    onValueChange={(value: ExpirePreset) =>
                      setCreateForm((prev) => ({ ...prev, expiresPreset: value }))
                    }
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {expirePresetOptions.map((option) => (
                        <SelectItem key={option.value} value={option.value}>
                          {isZh ? option.zh : option.en}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
            </div>

            <div className="h-px bg-slate-200/70" />

            <div className="space-y-3">
              <p className="text-sm font-semibold text-slate-700">{isZh ? "2. 访问权限" : "2. Access Permission"}</p>
              <div className="space-y-2">
                <FieldLabel>
                  {isZh
                    ? "绑定模型（不勾选=不可访问受控模型）"
                    : "Bind Models (none = deny on protected models)"}
                </FieldLabel>
                <MultiSelect
                  options={routeOptions}
                  values={createForm.model_ids}
                  placeholder={
                    isZh ? "选择可访问的受控模型" : "Select protected models this key can access"
                  }
                  searchPlaceholder={isZh ? "搜索模型..." : "Search models..."}
                  emptyText={isZh ? "无匹配模型" : "No matching models"}
                  onChange={(next) => setCreateForm((prev) => ({ ...prev, model_ids: next }))}
                />
              </div>
            </div>

            <div className="h-px bg-slate-200/70" />

            <div className="space-y-3">
              <p className="text-sm font-semibold text-slate-700">{isZh ? "3. 访问限额" : "3. Access Quota"}</p>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <FieldLabel>TPM</FieldLabel>
                  <Input
                    type="text"
                    inputMode="numeric"
                    pattern="[0-9]*"
                    value={createForm.tpm}
                    onChange={(e) => setCreateForm((prev) => ({ ...prev, tpm: digitsOnly(e.target.value) }))}
                    placeholder={isZh ? "留空=不限" : "Empty = unlimited"}
                  />
                </div>
                <div className="space-y-2">
                  <FieldLabel>TPD</FieldLabel>
                  <Input
                    type="text"
                    inputMode="numeric"
                    pattern="[0-9]*"
                    value={createForm.tpd}
                    onChange={(e) => setCreateForm((prev) => ({ ...prev, tpd: digitsOnly(e.target.value) }))}
                    placeholder={isZh ? "留空=不限" : "Empty = unlimited"}
                  />
                </div>
                <div className="space-y-2">
                  <FieldLabel>RPM</FieldLabel>
                  <Input
                    type="text"
                    inputMode="numeric"
                    pattern="[0-9]*"
                    value={createForm.rpm}
                    onChange={(e) => setCreateForm((prev) => ({ ...prev, rpm: digitsOnly(e.target.value) }))}
                    placeholder={isZh ? "留空=不限" : "Empty = unlimited"}
                  />
                </div>
                <div className="space-y-2">
                  <FieldLabel>RPD</FieldLabel>
                  <Input
                    type="text"
                    inputMode="numeric"
                    pattern="[0-9]*"
                    value={createForm.rpd}
                    onChange={(e) => setCreateForm((prev) => ({ ...prev, rpd: digitsOnly(e.target.value) }))}
                    placeholder={isZh ? "留空=不限" : "Empty = unlimited"}
                  />
                </div>
              </div>
            </div>
          </div>
          <div className="flex gap-3">
            <Button
              onClick={() =>
                createMut.mutate({
                  name: createForm.name.trim(),
                  rpm: createForm.rpm ? Number.parseInt(createForm.rpm, 10) : undefined,
                  rpd: createForm.rpd ? Number.parseInt(createForm.rpd, 10) : undefined,
                  tpm: createForm.tpm ? Number.parseInt(createForm.tpm, 10) : undefined,
                  tpd: createForm.tpd ? Number.parseInt(createForm.tpd, 10) : undefined,
                  expires_at: resolveExpiresAt(createForm.expiresPreset),
                  model_ids: createForm.model_ids,
                })
              }
              disabled={createMut.isPending || !createForm.name.trim()}
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
      ) : apiKeys.length === 0 ? (
        <div className="glass rounded-2xl p-12 text-center">
          <KeyRound className="mx-auto h-10 w-10 text-slate-400" />
          <p className="mt-3 text-sm text-slate-500">{isZh ? "还没有 API Key" : "No API keys yet"}</p>
        </div>
      ) : (
        <div className="grid gap-3">
          {pagedApiKeys.map((item) => {
            const isEditing = editingId === item.id && editForm;
            const keyExpired = isApiKeyExpired(item.expires_at);
            if (isEditing && editForm) {
              return (
                <div key={item.id} className="glass rounded-2xl p-5 space-y-4">
                  <div className="flex items-center justify-between">
                    <h3 className="text-sm font-semibold text-slate-900">{isZh ? "编辑 API Key" : "Edit API Key"}</h3>
                    <button
                      onClick={() => {
                        setEditingId(null);
                        setEditForm(null);
                        setCopiedEditKey(false);
                      }}
                      className="cursor-pointer p-1 text-slate-400 hover:text-slate-600"
                    >
                      <X className="h-4 w-4" />
                    </button>
                  </div>
                  <div className="space-y-5">
                    <div className="space-y-3">
                      <p className="text-sm font-semibold text-slate-700">{isZh ? "1. 基本信息" : "1. Basic Information"}</p>
                      <div className="grid grid-cols-2 gap-4">
                        <div className="space-y-2">
                          <FieldLabel>{isZh ? "名称" : "Name"}</FieldLabel>
                          <Input
                            value={editForm.name}
                            onChange={(e) => setEditForm((prev) => (prev ? { ...prev, name: e.target.value } : prev))}
                          />
                        </div>
                        <div className="space-y-2">
                          <FieldLabel>{isZh ? "有效期" : "Validity"}</FieldLabel>
                          <Input value={editForm.expires_text} disabled />
                        </div>
                        <div className="space-y-2">
                          <FieldLabel>{isZh ? "Key 值" : "Key Value"}</FieldLabel>
                          <div className="relative">
                            <Input value={editForm.key} disabled className="pr-10" />
                            <button
                              type="button"
                              onClick={async () => {
                                await navigator.clipboard.writeText(editForm.key);
                                setCopiedEditKey(true);
                                setTimeout(() => setCopiedEditKey(false), 1200);
                              }}
                              className="absolute top-1/2 right-3 -translate-y-1/2 text-slate-400 hover:text-slate-600 cursor-pointer"
                              title={isZh ? "复制 Key" : "Copy Key"}
                            >
                              <Copy className="h-4 w-4" />
                            </button>
                          </div>
                          {copiedEditKey && (
                            <p className="text-xs text-green-600">{isZh ? "已复制" : "Copied"}</p>
                          )}
                        </div>
                      </div>
                    </div>

                    <div className="h-px bg-slate-200/70" />

                    <div className="space-y-3">
                      <p className="text-sm font-semibold text-slate-700">{isZh ? "2. 访问权限" : "2. Access Permission"}</p>
                      <div className="space-y-2">
                        <FieldLabel>
                          {isZh
                            ? "绑定路由（不勾选=不可访问受控路由）"
                            : "Bind Routes (none = deny on protected routes)"}
                        </FieldLabel>
                        <MultiSelect
                          options={routeOptions}
                          values={editForm.model_ids}
                          placeholder={
                            isZh ? "选择可访问的受控模型" : "Select protected models this key can access"
                          }
                          searchPlaceholder={isZh ? "搜索路由..." : "Search routes..."}
                          emptyText={isZh ? "无匹配路由" : "No matching routes"}
                          onChange={(next) =>
                            setEditForm((prev) => (prev ? { ...prev, model_ids: next } : prev))
                          }
                        />
                      </div>
                    </div>

                    <div className="h-px bg-slate-200/70" />

                    <div className="space-y-3">
                      <p className="text-sm font-semibold text-slate-700">{isZh ? "3. 访问限额" : "3. Access Quota"}</p>
                      <div className="grid grid-cols-2 gap-4">
                        <div className="space-y-2">
                          <FieldLabel>TPM</FieldLabel>
                          <Input
                            type="text"
                            inputMode="numeric"
                            pattern="[0-9]*"
                            value={editForm.tpm}
                            onChange={(e) =>
                              setEditForm((prev) => (prev ? { ...prev, tpm: digitsOnly(e.target.value) } : prev))
                            }
                          />
                        </div>
                        <div className="space-y-2">
                          <FieldLabel>TPD</FieldLabel>
                          <Input
                            type="text"
                            inputMode="numeric"
                            pattern="[0-9]*"
                            value={editForm.tpd}
                            onChange={(e) =>
                              setEditForm((prev) => (prev ? { ...prev, tpd: digitsOnly(e.target.value) } : prev))
                            }
                          />
                        </div>
                        <div className="space-y-2">
                          <FieldLabel>RPM</FieldLabel>
                          <Input
                            type="text"
                            inputMode="numeric"
                            pattern="[0-9]*"
                            value={editForm.rpm}
                            onChange={(e) =>
                              setEditForm((prev) => (prev ? { ...prev, rpm: digitsOnly(e.target.value) } : prev))
                            }
                          />
                        </div>
                        <div className="space-y-2">
                          <FieldLabel>RPD</FieldLabel>
                          <Input
                            type="text"
                            inputMode="numeric"
                            pattern="[0-9]*"
                            value={editForm.rpd}
                            onChange={(e) =>
                              setEditForm((prev) => (prev ? { ...prev, rpd: digitsOnly(e.target.value) } : prev))
                            }
                          />
                        </div>
                      </div>
                    </div>
                  </div>
                  <div className="flex gap-3">
                    <Button
                      onClick={() =>
                        updateMut.mutate({
                          id: editForm.id,
                          input: {
                            name: editForm.name.trim(),
                            rpm: editForm.rpm ? Number.parseInt(editForm.rpm, 10) : 0,
                            rpd: editForm.rpd ? Number.parseInt(editForm.rpd, 10) : 0,
                            tpm: editForm.tpm ? Number.parseInt(editForm.tpm, 10) : 0,
                            tpd: editForm.tpd ? Number.parseInt(editForm.tpd, 10) : 0,
                            model_ids: editForm.model_ids,
                          },
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
                        setCopiedEditKey(false);
                      }}
                    >
                      {isZh ? "取消" : "Cancel"}
                    </Button>
                  </div>
                </div>
              );
            }

            return (
              <div key={item.id} className="glass flex items-center justify-between rounded-2xl p-4">
                <div className="flex items-center gap-3">
                  <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-slate-100">
                    <span className="inline-flex h-[30px] w-[30px] items-center justify-center rounded-xl border border-slate-300/70 bg-transparent">
                      <KeyRound className="h-3.5 w-3.5 text-slate-500" />
                    </span>
                  </div>
                  <div>
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="inline-flex h-5 items-center font-semibold text-slate-900">{item.name}</span>
                      <code className="inline-flex h-5 items-center rounded bg-slate-100 px-2 py-0.5 text-[10px] leading-none font-medium text-slate-600">
                        {shortApiKeyTag(item.key)}
                      </code>
                      {!item.is_enabled && (
                        <Badge variant="danger" className="connect-label-badge">
                          {isZh ? "已禁用" : "Disabled"}
                        </Badge>
                      )}
                      <Badge variant={keyExpired ? "danger" : "success"} className="connect-label-badge">
                        {formatValidityLabel(keyExpired, isZh)}
                      </Badge>
                      {item.model_ids.length > 0 && (
                        <Badge variant="warning" className="connect-label-badge bg-cyan-50 text-cyan-700">
                          {isZh ? `共 ${item.model_ids.length} 个模型` : `${item.model_ids.length} Models`}
                        </Badge>
                      )}
                      <Badge variant="warning" className="connect-label-badge bg-indigo-50 text-indigo-700">
                        RPM {quotaText(item.rpm)}
                      </Badge>
                      <Badge variant="warning" className="connect-label-badge bg-rose-50 text-rose-700">
                        RPD {quotaText(item.rpd)}
                      </Badge>
                      <Badge variant="warning" className="connect-label-badge bg-teal-50 text-teal-700">
                        TPM {quotaText(item.tpm)}
                      </Badge>
                      <Badge variant="warning" className="connect-label-badge bg-amber-50 text-amber-700">
                        TPD {quotaText(item.tpd)}
                      </Badge>
                    </div>
                  </div>
                </div>
                <div className="flex items-center gap-0.5">
                  <button
                    onClick={() => toggleEnabledMut.mutate({ id: item.id, is_enabled: !item.is_enabled })}
                    title={item.is_enabled ? (isZh ? "禁用" : "Disable") : (isZh ? "启用" : "Enable")}
                    className="cursor-pointer rounded-lg p-2 text-slate-400 transition-colors hover:bg-slate-100 hover:text-slate-600"
                  >
                    {item.is_enabled ? (
                      <ToggleRight className="h-4 w-4 text-green-500" />
                    ) : (
                      <ToggleLeft className="h-4 w-4 text-slate-400" />
                    )}
                  </button>
                  <button
                    onClick={() => copyKey(item)}
                    title={copiedId === item.id ? (isZh ? "复制成功" : "Copied") : (isZh ? "复制 Key" : "Copy Key")}
                    className={`cursor-pointer rounded-lg p-2 transition-colors ${
                      copiedId === item.id
                        ? "text-green-500 hover:bg-green-50 hover:text-green-500"
                        : "text-slate-400 hover:bg-slate-100 hover:text-slate-700"
                    }`}
                  >
                    {copiedId === item.id ? (
                      <Check className="h-4 w-4" />
                    ) : (
                      <Copy className="h-4 w-4" />
                    )}
                  </button>
                  <button
                    onClick={() => startEdit(item)}
                    className="cursor-pointer rounded-lg p-2 text-slate-400 transition-colors hover:bg-blue-50 hover:text-blue-500"
                  >
                    <Pencil className="h-4 w-4" />
                  </button>
                  <button
                    onClick={() => setApiKeyToDelete(item)}
                    className="cursor-pointer rounded-lg p-2 text-slate-400 transition-colors hover:bg-red-50 hover:text-red-500"
                  >
                    <Trash2 className="h-4 w-4" />
                  </button>
                </div>
              </div>
            );
          })}

          {apiKeys.length > PAGE_SIZE && (
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

      <Dialog
        open={showCreatedDialog}
        onOpenChange={(open) => {
          setShowCreatedDialog(open);
          if (!open) setCopiedCreatedKey(false);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{isZh ? "API Key 创建成功" : "API Key Created"}</DialogTitle>
            <DialogDescription>
              {isZh
                ? "请立即复制并保存该 Key，后续仅显示脱敏值。"
                : "Copy and save this key now. It will be masked in later views."}
            </DialogDescription>
          </DialogHeader>
          <div className="rounded-xl bg-slate-900 px-4 py-3 text-sm text-green-400">
            {createdKey ?? "-"}
          </div>
          <DialogFooter>
            <Button
              variant="secondary"
              onClick={() => {
                setShowCreatedDialog(false);
                setCopiedCreatedKey(false);
              }}
            >
              {isZh ? "关闭" : "Close"}
            </Button>
            <Button
              onClick={async () => {
                if (!createdKey) return;
                await navigator.clipboard.writeText(createdKey);
                setCopiedCreatedKey(true);
              }}
            >
              {copiedCreatedKey ? (isZh ? "已复制" : "Copied") : (isZh ? "复制" : "Copy")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={Boolean(apiKeyToDelete)}
        onOpenChange={(open) => {
          if (!open) setApiKeyToDelete(null);
        }}
        title={isZh ? "确认删除 API Key" : "Confirm API key deletion"}
        description={
          apiKeyToDelete
            ? (isZh
              ? `此操作不可撤销。确认删除「${apiKeyToDelete.name}」吗？`
              : `This action cannot be undone. Delete "${apiKeyToDelete.name}"?`)
            : undefined
        }
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={isZh ? "删除" : "Delete"}
        onConfirm={() => {
          if (!apiKeyToDelete) return;
          deleteMut.mutate(apiKeyToDelete.id);
          setApiKeyToDelete(null);
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
