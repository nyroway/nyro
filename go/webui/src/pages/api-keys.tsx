/* eslint-disable react-hooks/set-state-in-effect */

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Check,
  ChevronLeft,
  ChevronRight,
  Copy,
  KeyRound,
  Pencil,
  Plus,
  RotateCw,
  Trash2,
  ToggleRight,
  ToggleLeft,
  X,
} from "lucide-react";

import { backend } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type {
  Consumer,
  ConsumerKey,
  ConsumerQuota,
  CreateConsumer,
  CreateConsumerKey,
  CreateConsumerQuota,
  Route,
  UpdateConsumer,
  UpdateConsumerKey,
} from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { MultiSelect } from "@/components/ui/multi-select";
import { Combobox, type ComboboxOption } from "@/components/ui/combobox";
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

/** Backend only ever returns a fixed-length key prefix (never the full plaintext key,
 *  except the one-time reveal right after create/rotate), so there's nothing left to
 *  truncate here — just render it with a trailing ellipsis to signal "there's more". */
function formatKeyPrefix(prefix: string | undefined | null) {
  const trimmed = (prefix ?? "").trim();
  if (!trimmed) return "sk-";
  return `${trimmed}...`;
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

/** Same preset -> timestamp mapping as `resolveExpiresAt`, but for `UpdateConsumerKey`
 *  where the "not provided" and "clear it" cases are distinct: omitting the field means
 *  "leave unchanged", while an empty string means "clear to never expires". Editing a key's
 *  expiry to "never" must therefore send `""`, not `undefined`. */
function resolveExpiresAtForUpdate(preset: ExpirePreset) {
  return preset === "never" ? "" : resolveExpiresAt(preset);
}

function digitsOnly(value: string) {
  return value.replace(/[^\d]/g, "");
}

type QuotaRuleForm = { limit: string; window: string };

type QuotaFormState = {
  requests: QuotaRuleForm[];
  tokens: QuotaRuleForm[];
  concurrency: string;
};

const emptyQuotaForm: QuotaFormState = { requests: [], tokens: [], concurrency: "" };

function quotasToForm(quotas: ConsumerQuota[] | undefined): QuotaFormState {
  const form: QuotaFormState = { requests: [], tokens: [], concurrency: "" };
  for (const q of quotas ?? []) {
    if (q.quota_type === "requests") {
      form.requests.push({ limit: String(q.quota_limit), window: q.window ?? "" });
    } else if (q.quota_type === "tokens") {
      form.tokens.push({ limit: String(q.quota_limit), window: q.window ?? "" });
    } else if (q.quota_type === "concurrency") {
      form.concurrency = String(q.quota_limit);
    }
  }
  return form;
}

function buildQuotasPayload(form: QuotaFormState): CreateConsumerQuota[] {
  const quotas: CreateConsumerQuota[] = [];
  for (const row of form.requests) {
    if (!row.limit) continue;
    quotas.push({
      quota_type: "requests",
      quota_limit: Number.parseInt(row.limit, 10),
      window: row.window || undefined,
    });
  }
  for (const row of form.tokens) {
    if (!row.limit) continue;
    quotas.push({
      quota_type: "tokens",
      quota_limit: Number.parseInt(row.limit, 10),
      window: row.window || undefined,
    });
  }
  if (form.concurrency) {
    quotas.push({ quota_type: "concurrency", quota_limit: Number.parseInt(form.concurrency, 10) });
  }
  return quotas;
}

function quotaBadgeLabel(quotaType: string, isZh: boolean) {
  if (quotaType === "requests") return isZh ? "请求" : "req";
  if (quotaType === "tokens") return isZh ? "Token" : "tok";
  if (quotaType === "concurrency") return isZh ? "并发" : "conc";
  return quotaType;
}

const quotaBadgeColors = [
  "bg-indigo-50 text-indigo-700",
  "bg-rose-50 text-rose-700",
  "bg-teal-50 text-teal-700",
  "bg-amber-50 text-amber-700",
  "bg-fuchsia-50 text-fuchsia-700",
];

function QuotaEditor({
  value,
  onChange,
  isZh,
  windowOptions,
}: {
  value: QuotaFormState;
  onChange: (next: QuotaFormState) => void;
  isZh: boolean;
  windowOptions: ComboboxOption[];
}) {
  function updateRows(kind: "requests" | "tokens", rows: QuotaRuleForm[]) {
    onChange({ ...value, [kind]: rows });
  }

  function renderGroup(kind: "requests" | "tokens", title: string) {
    const rows = value[kind];
    return (
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <FieldLabel>{title}</FieldLabel>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={() => updateRows(kind, [...rows, { limit: "", window: "" }])}
          >
            <Plus className="h-3.5 w-3.5" />
            {isZh ? "添加一条" : "Add rule"}
          </Button>
        </div>
        {rows.length === 0 && (
          <p className="text-xs text-slate-400">{isZh ? "未设置，不限" : "Not set, unlimited"}</p>
        )}
        {rows.map((row, idx) => (
          <div key={idx} className="flex items-center gap-2">
            <Input
              type="text"
              inputMode="numeric"
              pattern="[0-9]*"
              value={row.limit}
              onChange={(e) => {
                const next = rows.slice();
                next[idx] = { ...row, limit: digitsOnly(e.target.value) };
                updateRows(kind, next);
              }}
              placeholder={isZh ? "上限次数" : "limit"}
              className="flex-1"
            />
            <div className="w-32 shrink-0">
              <Combobox
                options={windowOptions}
                value={row.window}
                onValueChange={(w) => {
                  const next = rows.slice();
                  next[idx] = { ...row, window: w };
                  updateRows(kind, next);
                }}
                allowCustom
                placeholder={isZh ? "窗口" : "window"}
              />
            </div>
            <button
              type="button"
              onClick={() => updateRows(kind, rows.filter((_, i) => i !== idx))}
              className="cursor-pointer p-1 text-slate-400 hover:text-red-500"
              title={isZh ? "删除该条限额" : "Remove rule"}
            >
              <Trash2 className="h-4 w-4" />
            </button>
          </div>
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {renderGroup("requests", isZh ? "请求次数限额 (requests)" : "Request quota (requests)")}
      {renderGroup("tokens", isZh ? "Token 用量限额 (tokens)" : "Token quota (tokens)")}
      <div className="space-y-2">
        <FieldLabel>{isZh ? "并发上限 (concurrency)" : "Concurrency limit"}</FieldLabel>
        <Input
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          value={value.concurrency}
          onChange={(e) => onChange({ ...value, concurrency: digitsOnly(e.target.value) })}
          placeholder={isZh ? "留空=不限，同时处理的最大请求数" : "Empty = unlimited, max in-flight requests"}
        />
      </div>
    </div>
  );
}

type CreateForm = {
  name: string;
  routes: string[];
  quotas: QuotaFormState;
  keyName: string;
  keyExpiresPreset: ExpirePreset;
};

const emptyCreate: CreateForm = {
  name: "",
  routes: [],
  quotas: emptyQuotaForm,
  keyName: "",
  keyExpiresPreset: "never",
};

type EditForm = {
  id: string;
  name: string;
  enabled: boolean;
  routes: string[];
  quotas: QuotaFormState;
};

type RevealedKey = { name: string; token: string };

export default function ApiKeysPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const qc = useQueryClient();

  const windowOptions = useMemo<ComboboxOption[]>(
    () => [
      { value: "", label: isZh ? "不区分窗口" : "No window" },
      { value: "1m", label: "1m" },
      { value: "5m", label: "5m" },
      { value: "15m", label: "15m" },
      { value: "1h", label: "1h" },
      { value: "6h", label: "6h" },
      { value: "12h", label: "12h" },
      { value: "1d", label: "1d" },
    ],
    [isZh],
  );

  const [showForm, setShowForm] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [createForm, setCreateForm] = useState<CreateForm>(emptyCreate);
  const [editForm, setEditForm] = useState<EditForm | null>(null);
  const [page, setPage] = useState(0);

  const [revealedKey, setRevealedKey] = useState<RevealedKey | null>(null);
  const [showRevealDialog, setShowRevealDialog] = useState(false);
  const [copiedRevealKey, setCopiedRevealKey] = useState(false);

  const [copiedKeyId, setCopiedKeyId] = useState<string | null>(null);

  const [addKeyOpenFor, setAddKeyOpenFor] = useState<string | null>(null);
  const [addKeyForm, setAddKeyForm] = useState<{ name: string; expiresPreset: ExpirePreset }>({
    name: "",
    expiresPreset: "never",
  });

  const [keyExpiryEdit, setKeyExpiryEdit] = useState<{
    consumerId: string;
    keyId: string;
    preset: ExpirePreset;
  } | null>(null);

  const [consumerToDelete, setConsumerToDelete] = useState<Consumer | null>(null);
  const [keyToDelete, setKeyToDelete] = useState<{ consumer: Consumer; key: ConsumerKey } | null>(null);
  const [keyToRotate, setKeyToRotate] = useState<{ consumer: Consumer; key: ConsumerKey } | null>(null);
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

  function openRevealDialog(key: RevealedKey) {
    setRevealedKey(key);
    setCopiedRevealKey(false);
    setShowRevealDialog(true);
  }

  const { data: consumers = [], isLoading } = useQuery<Consumer[]>({
    queryKey: ["consumers"],
    queryFn: () => backend("list_consumers"),
  });
  const { data: routes = [] } = useQuery<Route[]>({
    queryKey: ["routes"],
    queryFn: () => backend("list_routes"),
  });

  function invalidateConsumers() {
    return qc.invalidateQueries({ queryKey: ["consumers"] });
  }

  const createMut = useMutation({
    mutationFn: (input: CreateConsumer) => backend<Consumer>("create_consumer", { input }),
    onSuccess: (created) => {
      invalidateConsumers();
      setShowForm(false);
      setCreateForm(emptyCreate);
      const firstKey = created.keys?.[0];
      if (firstKey?.token) {
        openRevealDialog({ name: firstKey.name, token: firstKey.token });
      }
    },
    onError: (error: unknown) => {
      showErrorDialog("创建 Consumer 失败", "Failed to create consumer", error);
    },
  });

  const updateMut = useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateConsumer }) =>
      backend<Consumer>("update_consumer", { id, input }),
    onSuccess: () => {
      invalidateConsumers();
      setEditingId(null);
      setEditForm(null);
    },
    onError: (error: unknown) => {
      showErrorDialog("保存 Consumer 失败", "Failed to save consumer", error);
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => backend("delete_consumer", { id }),
    onSuccess: () => invalidateConsumers(),
    onError: (error: unknown) => {
      showErrorDialog("删除 Consumer 失败", "Failed to delete consumer", error);
    },
  });

  const toggleConsumerEnabledMut = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      backend("update_consumer", { id, input: { enabled } }),
    onSuccess: () => invalidateConsumers(),
    onError: (error: unknown) => {
      showErrorDialog("操作失败", "Operation failed", error);
    },
  });

  const addKeyMut = useMutation({
    mutationFn: ({ consumerId, input }: { consumerId: string; input: CreateConsumerKey }) =>
      backend<ConsumerKey>("add_consumer_key", { id: consumerId, input }),
    onSuccess: (created) => {
      invalidateConsumers();
      setAddKeyOpenFor(null);
      setAddKeyForm({ name: "", expiresPreset: "never" });
      if (created.token) {
        openRevealDialog({ name: created.name, token: created.token });
      }
    },
    onError: (error: unknown) => {
      showErrorDialog("新增 Key 失败", "Failed to add key", error);
    },
  });

  const updateKeyMut = useMutation({
    mutationFn: ({
      consumerId,
      keyId,
      input,
    }: {
      consumerId: string;
      keyId: string;
      input: UpdateConsumerKey;
    }) => backend<ConsumerKey>("update_consumer_key", { id: consumerId, keyId, input }),
    onSuccess: () => invalidateConsumers(),
    onError: (error: unknown) => {
      showErrorDialog("更新 Key 失败", "Failed to update key", error);
    },
  });

  const deleteKeyMut = useMutation({
    mutationFn: ({ consumerId, keyId }: { consumerId: string; keyId: string }) =>
      backend("delete_consumer_key", { id: consumerId, keyId }),
    onSuccess: () => invalidateConsumers(),
    onError: (error: unknown) => {
      showErrorDialog("删除 Key 失败", "Failed to delete key", error);
    },
  });

  const rotateKeyMut = useMutation({
    mutationFn: async ({ consumerId, key }: { consumerId: string; key: ConsumerKey }) => {
      const created = await backend<ConsumerKey>("add_consumer_key", {
        id: consumerId,
        input: { name: key.name, expires_at: key.expires_at },
      });
      await backend("delete_consumer_key", { id: consumerId, keyId: key.id });
      return created;
    },
    onSuccess: (created) => {
      invalidateConsumers();
      if (created.token) {
        openRevealDialog({ name: created.name, token: created.token });
      }
    },
    onError: (error: unknown) => {
      showErrorDialog("轮换 Key 失败", "Failed to rotate key", error);
    },
  });

  const totalPages = Math.max(1, Math.ceil(consumers.length / PAGE_SIZE));
  const pagedConsumers = consumers.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);

  useEffect(() => {
    if (page > totalPages - 1) setPage(0);
  }, [page, totalPages]);

  // P0 fix: the backend resolves a consumer's route bindings by route *model name*
  // (see `resolveRouteIDsByModel` in the database/memory stores), not by route id.
  // `Consumer.routes` / `CreateConsumer.routes` / `UpdateConsumer.routes` are all
  // `string[]` of model names, so the option value here must be `route.model`.
  const routeOptions = useMemo(
    () =>
      routes.map((route) => ({
        value: route.model,
        label: route.model,
      })),
    [routes],
  );

  function startEdit(item: Consumer) {
    setEditingId(item.id);
    setEditForm({
      id: item.id,
      name: item.name,
      enabled: item.enabled,
      routes: item.routes ?? [],
      quotas: quotasToForm(item.quotas),
    });
  }

  async function copyKeyPrefix(key: ConsumerKey) {
    await navigator.clipboard.writeText(key.key_prefix);
    setCopiedKeyId(key.id);
    setTimeout(() => setCopiedKeyId(null), 1500);
  }

  function renderQuotaBadges(quotas: ConsumerQuota[] | undefined) {
    return (quotas ?? []).map((q, idx) => {
      const label = quotaBadgeLabel(q.quota_type, isZh);
      const text = q.window ? `${label} ${q.quota_limit}/${q.window}` : `${label} ${q.quota_limit}`;
      return (
        <Badge
          key={q.id ?? `${q.quota_type}-${idx}`}
          variant="warning"
          className={`connect-label-badge ${quotaBadgeColors[idx % quotaBadgeColors.length]}`}
        >
          {text}
        </Badge>
      );
    });
  }

  function renderKeyRow(consumer: Consumer, key: ConsumerKey) {
    const keyExpired = isApiKeyExpired(key.expires_at);
    const isEditingExpiry = keyExpiryEdit?.consumerId === consumer.id && keyExpiryEdit.keyId === key.id;
    return (
      <div key={key.id} className="flex items-center justify-between rounded-xl border border-slate-200/70 bg-white/60 px-3 py-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="inline-flex h-5 items-center font-medium text-slate-800">{key.name}</span>
          <code className="inline-flex h-5 items-center rounded bg-slate-100 px-2 py-0.5 text-[10px] leading-none font-medium text-slate-600">
            {formatKeyPrefix(key.key_prefix)}
          </code>
          {!key.enabled && (
            <Badge variant="danger" className="connect-label-badge">
              {isZh ? "已禁用" : "Disabled"}
            </Badge>
          )}
          <Badge variant={keyExpired ? "danger" : "success"} className="connect-label-badge">
            {formatValidityLabel(keyExpired, isZh)}
          </Badge>
          {isEditingExpiry ? (
            <div className="flex items-center gap-1">
              <Select
                value={keyExpiryEdit.preset}
                onValueChange={(value: ExpirePreset) =>
                  setKeyExpiryEdit((prev) => (prev ? { ...prev, preset: value } : prev))
                }
              >
                <SelectTrigger className="h-7 w-32 text-xs">
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
              <Button
                size="sm"
                onClick={() => {
                  updateKeyMut.mutate({
                    consumerId: consumer.id,
                    keyId: key.id,
                    input: { expires_at: resolveExpiresAtForUpdate(keyExpiryEdit.preset) },
                  });
                  setKeyExpiryEdit(null);
                }}
              >
                {isZh ? "保存" : "Save"}
              </Button>
              <Button size="sm" variant="secondary" onClick={() => setKeyExpiryEdit(null)}>
                {isZh ? "取消" : "Cancel"}
              </Button>
            </div>
          ) : (
            <button
              type="button"
              onClick={() =>
                setKeyExpiryEdit({ consumerId: consumer.id, keyId: key.id, preset: "never" })
              }
              className="cursor-pointer text-xs text-slate-500 underline decoration-dotted hover:text-slate-700"
              title={isZh ? "点击修改过期时间" : "Click to edit expiry"}
            >
              {formatExpiresText(key.expires_at, isZh)}
            </button>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-0.5">
          <button
            onClick={() => updateKeyMut.mutate({ consumerId: consumer.id, keyId: key.id, input: { enabled: !key.enabled } })}
            title={key.enabled ? (isZh ? "禁用" : "Disable") : (isZh ? "启用" : "Enable")}
            className="cursor-pointer rounded-lg p-1.5 text-slate-400 transition-colors hover:bg-slate-100 hover:text-slate-600"
          >
            {key.enabled ? (
              <ToggleRight className="h-4 w-4 text-green-500" />
            ) : (
              <ToggleLeft className="h-4 w-4 text-slate-400" />
            )}
          </button>
          <button
            onClick={() => copyKeyPrefix(key)}
            title={
              copiedKeyId === key.id
                ? (isZh ? "已复制前缀" : "Prefix copied")
                : (isZh
                  ? "复制前缀（完整 Key 仅在创建/轮换时展示一次，此处无法复制完整 Key）"
                  : "Copy prefix only (full key is shown once on create/rotate, not copyable here)")
            }
            className={`cursor-pointer rounded-lg p-1.5 transition-colors ${
              copiedKeyId === key.id
                ? "text-green-500 hover:bg-green-50"
                : "text-slate-400 hover:bg-slate-100 hover:text-slate-700"
            }`}
          >
            {copiedKeyId === key.id ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
          </button>
          <button
            onClick={() => setKeyToRotate({ consumer, key })}
            title={isZh ? "轮换 Key（旧 Key 将立即失效）" : "Rotate key (old key is invalidated immediately)"}
            className="cursor-pointer rounded-lg p-1.5 text-slate-400 transition-colors hover:bg-blue-50 hover:text-blue-500"
          >
            <RotateCw className="h-4 w-4" />
          </button>
          <button
            onClick={() => setKeyToDelete({ consumer, key })}
            className="cursor-pointer rounded-lg p-1.5 text-slate-400 transition-colors hover:bg-red-50 hover:text-red-500"
          >
            <Trash2 className="h-4 w-4" />
          </button>
        </div>
      </div>
    );
  }

  function renderKeysSection(consumer: Consumer) {
    const keys = consumer.keys ?? [];
    const isAddingKey = addKeyOpenFor === consumer.id;
    return (
      <div className="space-y-2 rounded-xl bg-slate-50/60 p-3">
        <div className="flex items-center justify-between">
          <span className="text-xs font-semibold text-slate-600">
            {isZh ? `密钥（共 ${keys.length} 把）` : `Keys (${keys.length})`}
          </span>
          <Button
            type="button"
            size="sm"
            variant="secondary"
            onClick={() => {
              setAddKeyOpenFor(isAddingKey ? null : consumer.id);
              setAddKeyForm({ name: "", expiresPreset: "never" });
            }}
          >
            <Plus className="h-3.5 w-3.5" />
            {isZh ? "新增 Key" : "Add key"}
          </Button>
        </div>

        {keys.length === 0 && !isAddingKey && (
          <p className="text-xs text-slate-400">
            {isZh ? "该 consumer 暂无可用密钥，无法鉴权。" : "No keys yet — this consumer cannot authenticate."}
          </p>
        )}

        <div className="space-y-1.5">{keys.map((key) => renderKeyRow(consumer, key))}</div>

        {isAddingKey && (
          <div className="flex flex-wrap items-center gap-2 rounded-xl border border-dashed border-slate-300 p-2">
            <Input
              value={addKeyForm.name}
              onChange={(e) => setAddKeyForm((prev) => ({ ...prev, name: e.target.value }))}
              placeholder={isZh ? "Key 名称" : "Key name"}
              className="h-8 w-40 text-xs"
            />
            <Select
              value={addKeyForm.expiresPreset}
              onValueChange={(value: ExpirePreset) => setAddKeyForm((prev) => ({ ...prev, expiresPreset: value }))}
            >
              <SelectTrigger className="h-8 w-28 text-xs">
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
            <Button
              size="sm"
              disabled={addKeyMut.isPending || !addKeyForm.name.trim()}
              onClick={() =>
                addKeyMut.mutate({
                  consumerId: consumer.id,
                  input: {
                    name: addKeyForm.name.trim(),
                    expires_at: resolveExpiresAt(addKeyForm.expiresPreset),
                  },
                })
              }
            >
              {addKeyMut.isPending ? (isZh ? "添加中..." : "Adding...") : (isZh ? "添加" : "Add")}
            </Button>
            <Button size="sm" variant="secondary" onClick={() => setAddKeyOpenFor(null)}>
              {isZh ? "取消" : "Cancel"}
            </Button>
          </div>
        )}
      </div>
    );
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
          <h2 className="text-lg font-semibold text-slate-900">{isZh ? "创建 Consumer" : "Create Consumer"}</h2>
          <div className="space-y-5">
            <div className="space-y-3">
              <p className="text-sm font-semibold text-slate-700">{isZh ? "1. 基本信息" : "1. Basic Information"}</p>
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
                  <FieldLabel>{isZh ? "首个 Key 名称" : "First key name"}</FieldLabel>
                  <Input
                    value={createForm.keyName}
                    onChange={(e) => setCreateForm((prev) => ({ ...prev, keyName: e.target.value }))}
                    placeholder={isZh ? "例如 default" : "e.g. default"}
                  />
                </div>
                <div className="space-y-2">
                  <FieldLabel>{isZh ? "首个 Key 有效期" : "First key validity"}</FieldLabel>
                  <Select
                    value={createForm.keyExpiresPreset}
                    onValueChange={(value: ExpirePreset) =>
                      setCreateForm((prev) => ({ ...prev, keyExpiresPreset: value }))
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
              <p className="text-xs text-slate-500">
                {isZh
                  ? "Key 值会在创建后自动生成，仅在创建成功后展示一次。"
                  : "The key value is auto-generated and shown exactly once after creation."}
              </p>
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
                  values={createForm.routes}
                  placeholder={
                    isZh ? "选择可访问的受控模型" : "Select protected models this key can access"
                  }
                  searchPlaceholder={isZh ? "搜索模型..." : "Search models..."}
                  emptyText={isZh ? "无匹配模型" : "No matching models"}
                  onChange={(next) => setCreateForm((prev) => ({ ...prev, routes: next }))}
                />
              </div>
            </div>

            <div className="h-px bg-slate-200/70" />

            <div className="space-y-3">
              <p className="text-sm font-semibold text-slate-700">{isZh ? "3. 访问限额" : "3. Access Quota"}</p>
              <QuotaEditor
                value={createForm.quotas}
                onChange={(next) => setCreateForm((prev) => ({ ...prev, quotas: next }))}
                isZh={isZh}
                windowOptions={windowOptions}
              />
            </div>
          </div>
          <div className="flex gap-3">
            <Button
              onClick={() =>
                createMut.mutate({
                  name: createForm.name.trim(),
                  routes: createForm.routes,
                  quotas: buildQuotasPayload(createForm.quotas),
                  keys: [
                    {
                      name: createForm.keyName.trim() || (isZh ? "默认 Key" : "Default key"),
                      expires_at: resolveExpiresAt(createForm.keyExpiresPreset),
                    },
                  ],
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
      ) : consumers.length === 0 ? (
        <div className="glass rounded-2xl p-12 text-center">
          <KeyRound className="mx-auto h-10 w-10 text-slate-400" />
          <p className="mt-3 text-sm text-slate-500">{isZh ? "还没有 API Key" : "No API keys yet"}</p>
        </div>
      ) : (
        <div className="grid gap-3">
          {pagedConsumers.map((item) => {
            const isEditing = editingId === item.id && editForm;

            if (isEditing && editForm) {
              return (
                <div key={item.id} className="glass rounded-2xl p-5 space-y-4">
                  <div className="flex items-center justify-between">
                    <h3 className="text-sm font-semibold text-slate-900">{isZh ? "编辑 Consumer" : "Edit Consumer"}</h3>
                    <button
                      onClick={() => {
                        setEditingId(null);
                        setEditForm(null);
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
                          <FieldLabel>{isZh ? "状态" : "Status"}</FieldLabel>
                          <button
                            type="button"
                            onClick={() => setEditForm((prev) => (prev ? { ...prev, enabled: !prev.enabled } : prev))}
                            className="flex h-10 cursor-pointer items-center gap-2 rounded-md border border-input px-3 text-sm"
                          >
                            {editForm.enabled ? (
                              <ToggleRight className="h-4 w-4 text-green-500" />
                            ) : (
                              <ToggleLeft className="h-4 w-4 text-slate-400" />
                            )}
                            {editForm.enabled ? (isZh ? "已启用" : "Enabled") : (isZh ? "已禁用" : "Disabled")}
                          </button>
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
                          values={editForm.routes}
                          placeholder={
                            isZh ? "选择可访问的受控模型" : "Select protected models this key can access"
                          }
                          searchPlaceholder={isZh ? "搜索模型..." : "Search models..."}
                          emptyText={isZh ? "无匹配模型" : "No matching models"}
                          onChange={(next) => setEditForm((prev) => (prev ? { ...prev, routes: next } : prev))}
                        />
                      </div>
                    </div>

                    <div className="h-px bg-slate-200/70" />

                    <div className="space-y-3">
                      <p className="text-sm font-semibold text-slate-700">{isZh ? "3. 访问限额" : "3. Access Quota"}</p>
                      <QuotaEditor
                        value={editForm.quotas}
                        onChange={(next) => setEditForm((prev) => (prev ? { ...prev, quotas: next } : prev))}
                        isZh={isZh}
                        windowOptions={windowOptions}
                      />
                    </div>
                  </div>
                  <div className="flex gap-3">
                    <Button
                      onClick={() =>
                        updateMut.mutate({
                          id: editForm.id,
                          input: {
                            name: editForm.name.trim(),
                            enabled: editForm.enabled,
                            routes: editForm.routes,
                            quotas: buildQuotasPayload(editForm.quotas),
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
                      }}
                    >
                      {isZh ? "取消" : "Cancel"}
                    </Button>
                  </div>

                  <div className="h-px bg-slate-200/70" />
                  {renderKeysSection(item)}
                </div>
              );
            }

            return (
              <div key={item.id} className="glass rounded-2xl p-4 space-y-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-3">
                    <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-slate-100">
                      <span className="inline-flex h-[30px] w-[30px] items-center justify-center rounded-xl border border-slate-300/70 bg-transparent">
                        <KeyRound className="h-3.5 w-3.5 text-slate-500" />
                      </span>
                    </div>
                    <div>
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="inline-flex h-5 items-center font-semibold text-slate-900">{item.name}</span>
                        {!item.enabled && (
                          <Badge variant="danger" className="connect-label-badge">
                            {isZh ? "已禁用" : "Disabled"}
                          </Badge>
                        )}
                        {(item.routes ?? []).map((model) => (
                          <Badge key={model} variant="warning" className="connect-label-badge bg-cyan-50 text-cyan-700">
                            {model}
                          </Badge>
                        ))}
                        {renderQuotaBadges(item.quotas)}
                      </div>
                    </div>
                  </div>
                  <div className="flex items-center gap-0.5">
                    <button
                      onClick={() => toggleConsumerEnabledMut.mutate({ id: item.id, enabled: !item.enabled })}
                      title={item.enabled ? (isZh ? "禁用" : "Disable") : (isZh ? "启用" : "Enable")}
                      className="cursor-pointer rounded-lg p-2 text-slate-400 transition-colors hover:bg-slate-100 hover:text-slate-600"
                    >
                      {item.enabled ? (
                        <ToggleRight className="h-4 w-4 text-green-500" />
                      ) : (
                        <ToggleLeft className="h-4 w-4 text-slate-400" />
                      )}
                    </button>
                    <button
                      onClick={() => startEdit(item)}
                      className="cursor-pointer rounded-lg p-2 text-slate-400 transition-colors hover:bg-blue-50 hover:text-blue-500"
                    >
                      <Pencil className="h-4 w-4" />
                    </button>
                    <button
                      onClick={() => setConsumerToDelete(item)}
                      className="cursor-pointer rounded-lg p-2 text-slate-400 transition-colors hover:bg-red-50 hover:text-red-500"
                    >
                      <Trash2 className="h-4 w-4" />
                    </button>
                  </div>
                </div>

                {renderKeysSection(item)}
              </div>
            );
          })}

          {consumers.length > PAGE_SIZE && (
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
        open={showRevealDialog}
        onOpenChange={(open) => {
          setShowRevealDialog(open);
          if (!open) setCopiedRevealKey(false);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{isZh ? "Key 已生成" : "Key Generated"}</DialogTitle>
            <DialogDescription>
              {isZh
                ? `「${revealedKey?.name ?? ""}」的完整 Key 仅在此处显示一次，请立即复制并妥善保存，关闭后将无法再次查看明文。`
                : `The full key for "${revealedKey?.name ?? ""}" is shown only once here. Copy and save it now — it cannot be viewed again after closing.`}
            </DialogDescription>
          </DialogHeader>
          <div className="rounded-xl bg-slate-900 px-4 py-3 text-sm break-all text-green-400">
            {revealedKey?.token ?? "-"}
          </div>
          <DialogFooter>
            <Button
              variant="secondary"
              onClick={() => {
                setShowRevealDialog(false);
                setCopiedRevealKey(false);
              }}
            >
              {isZh ? "关闭" : "Close"}
            </Button>
            <Button
              onClick={async () => {
                if (!revealedKey?.token) return;
                await navigator.clipboard.writeText(revealedKey.token);
                setCopiedRevealKey(true);
              }}
            >
              {copiedRevealKey ? (isZh ? "已复制" : "Copied") : (isZh ? "复制" : "Copy")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={Boolean(consumerToDelete)}
        onOpenChange={(open) => {
          if (!open) setConsumerToDelete(null);
        }}
        title={isZh ? "确认删除 Consumer" : "Confirm consumer deletion"}
        description={
          consumerToDelete
            ? (isZh
              ? `此操作不可撤销，将同时删除其名下所有 Key。确认删除「${consumerToDelete.name}」吗？`
              : `This action cannot be undone and will delete all of its keys. Delete "${consumerToDelete.name}"?`)
            : undefined
        }
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={isZh ? "删除" : "Delete"}
        onConfirm={() => {
          if (!consumerToDelete) return;
          deleteMut.mutate(consumerToDelete.id);
          setConsumerToDelete(null);
        }}
      />

      <ConfirmDialog
        open={Boolean(keyToRotate)}
        onOpenChange={(open) => {
          if (!open) setKeyToRotate(null);
        }}
        title={isZh ? "确认轮换 Key" : "Confirm key rotation"}
        description={
          keyToRotate
            ? (isZh
              ? `轮换后旧 Key「${keyToRotate.key.name}」将立即失效，新 Key 会以相同名称生成，完整值仅显示一次。确认继续吗？`
              : `Rotating will immediately invalidate the old key "${keyToRotate.key.name}". A new key with the same name will be generated and its full value shown once. Continue?`)
            : undefined
        }
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={isZh ? "轮换" : "Rotate"}
        onConfirm={() => {
          if (!keyToRotate) return;
          rotateKeyMut.mutate({ consumerId: keyToRotate.consumer.id, key: keyToRotate.key });
          setKeyToRotate(null);
        }}
      />

      <ConfirmDialog
        open={Boolean(keyToDelete)}
        onOpenChange={(open) => {
          if (!open) setKeyToDelete(null);
        }}
        title={isZh ? "确认删除 Key" : "Confirm key deletion"}
        description={
          keyToDelete
            ? ((keyToDelete.consumer.keys?.length ?? 0) <= 1
              ? (isZh
                ? `「${keyToDelete.key.name}」是「${keyToDelete.consumer.name}」目前唯一的 Key，删除后该 consumer 将没有可用凭证，无法鉴权。确认删除吗？`
                : `"${keyToDelete.key.name}" is the only key for "${keyToDelete.consumer.name}". Deleting it leaves this consumer with no usable credential. Delete anyway?`)
              : (isZh
                ? `此操作不可撤销。确认删除「${keyToDelete.key.name}」吗？`
                : `This action cannot be undone. Delete "${keyToDelete.key.name}"?`))
            : undefined
        }
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={isZh ? "删除" : "Delete"}
        onConfirm={() => {
          if (!keyToDelete) return;
          deleteKeyMut.mutate({ consumerId: keyToDelete.consumer.id, keyId: keyToDelete.key.id });
          setKeyToDelete(null);
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
