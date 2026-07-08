/* eslint-disable react-hooks/set-state-in-effect */

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ChevronLeft,
  ChevronRight,
  Info,
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
  ConsumerLimits,
  ConsumerQuota,
  CreateConsumer,
  CreateConsumerKey,
  CreateConsumerQuota,
  Route,
  UpdateConsumer,
  UpdateConsumerKey,
} from "@/lib/types";
import { PROTOCOL_TABLE } from "@/lib/protocol";
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
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

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

function FieldLabel({
  children,
  info,
  required,
}: {
  children: string;
  info?: string;
  required?: boolean;
}) {
  return (
    <label className="ml-1 inline-flex items-center gap-1 text-xs leading-none font-normal text-slate-900">
      <span>{children}</span>
      {required ? (
        <span className="text-red-500">
          <span aria-hidden="true">*</span>
          <span className="sr-only">required</span>
        </span>
      ) : null}
      {info ? (
        <TooltipProvider delayDuration={120}>
          <Tooltip>
            <TooltipTrigger asChild>
              <span
                className="inline-flex cursor-help text-slate-400 hover:text-slate-600"
                aria-label={info}
              >
                <Info className="h-3.5 w-3.5" />
              </span>
            </TooltipTrigger>
            <TooltipContent>{info}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      ) : null}
    </label>
  );
}

function SectionTitle({ children, info }: { children: string; info?: string }) {
  return (
    <div className="flex items-center gap-1">
      <p className="text-sm font-semibold text-slate-700">{children}</p>
      {info ? (
        <TooltipProvider delayDuration={120}>
          <Tooltip>
            <TooltipTrigger asChild>
              <span
                className="inline-flex cursor-help text-slate-400 hover:text-slate-600"
                aria-label={info}
              >
                <Info className="h-3.5 w-3.5" />
              </span>
            </TooltipTrigger>
            <TooltipContent>{info}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      ) : null}
    </div>
  );
}

/** Backend only ever returns a masked key_preview (leading 12 + trailing 6
 *  characters of the raw key, concatenated — never the full plaintext, except
 *  the one-time reveal right after create/add/regenerate). This renders it as
 *  "sk-abc123************************abc123": a fixed-length mask so the
 *  asterisk run never hints at the real key's length. */
const KEY_PREVIEW_LEAD_VISIBLE = 9;
const KEY_PREVIEW_TRAIL_VISIBLE = 6;
const KEY_PREVIEW_MASK_LEN = 28;

function formatKeyPreview(preview: string | undefined | null) {
  const trimmed = (preview ?? "").trim();
  if (!trimmed) return "sk-";
  if (trimmed.length <= KEY_PREVIEW_LEAD_VISIBLE + KEY_PREVIEW_TRAIL_VISIBLE) return trimmed;
  const lead = trimmed.slice(0, KEY_PREVIEW_LEAD_VISIBLE);
  const trail = trimmed.slice(-KEY_PREVIEW_TRAIL_VISIBLE);
  return `${lead}${"*".repeat(KEY_PREVIEW_MASK_LEN)}${trail}`;
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

const emptyQuotaRow: QuotaRuleForm = { limit: "", window: "" };

/** buildQuotasPayload drops any row with an empty limit, so a default blank
 *  row is purely a UI convenience — it never reaches the submitted payload
 *  unless the user actually fills in a limit. */
const emptyQuotaForm: QuotaFormState = { requests: [{ ...emptyQuotaRow }], tokens: [{ ...emptyQuotaRow }], concurrency: "" };

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
  if (form.requests.length === 0) form.requests.push({ ...emptyQuotaRow });
  if (form.tokens.length === 0) form.tokens.push({ ...emptyQuotaRow });
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

type LimitsFormState = { maxInputTokens: string; maxOutputTokens: string; maxRequestBodyBytes: string };

const emptyLimitsForm: LimitsFormState = { maxInputTokens: "", maxOutputTokens: "", maxRequestBodyBytes: "" };

function limitsToForm(limits: ConsumerLimits | undefined): LimitsFormState {
  return {
    maxInputTokens: limits?.max_input_tokens ? String(limits.max_input_tokens) : "",
    maxOutputTokens: limits?.max_output_tokens ? String(limits.max_output_tokens) : "",
    maxRequestBodyBytes: limits?.max_request_body_bytes ? String(limits.max_request_body_bytes) : "",
  };
}

/** Returns undefined (omit `limits` entirely) when every field is empty, rather
 *  than sending an all-zero object — zero on a single field already means "no
 *  limit" for that dimension, so an empty form should not touch the others. */
function buildLimitsPayload(form: LimitsFormState): ConsumerLimits | undefined {
  if (!form.maxInputTokens && !form.maxOutputTokens && !form.maxRequestBodyBytes) return undefined;
  return {
    max_input_tokens: form.maxInputTokens ? Number.parseInt(form.maxInputTokens, 10) : undefined,
    max_output_tokens: form.maxOutputTokens ? Number.parseInt(form.maxOutputTokens, 10) : undefined,
    max_request_body_bytes: form.maxRequestBodyBytes ? Number.parseInt(form.maxRequestBodyBytes, 10) : undefined,
  };
}

/** access.ip_allowlist is edited as one input row per entry (like the
 *  requests/tokens quota rows), each holding a single IP or CIDR block. */
function ipAllowlistToForm(list: string[] | undefined): string[] {
  return list && list.length > 0 ? [...list] : [""];
}

/** buildAccessListPayload drops blank rows, mirroring buildQuotasPayload's
 *  skip-empty-limit behavior — an untouched blank row never reaches the
 *  submitted payload. */
function buildAccessListPayload(rows: string[]): string[] {
  return rows.map((r) => r.trim()).filter(Boolean);
}

function isValidIPv4(addr: string): boolean {
  const parts = addr.split(".");
  if (parts.length !== 4) return false;
  return parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) >= 0 && Number(p) <= 255);
}

function isValidIPv6(addr: string): boolean {
  if (!/^[0-9a-fA-F:]+$/.test(addr)) return false;
  if ((addr.match(/::/g) ?? []).length > 1) return false;
  const groups = addr.split(":").filter((g, i, arr) => !(g === "" && (i === 0 || i === arr.length - 1)));
  return groups.length > 0 && groups.length <= 8 && groups.every((g) => /^[0-9a-fA-F]{1,4}$/.test(g));
}

/** Accepts a bare IP (v4 or v6) or a CIDR block (IP + "/" + prefix length).
 *  An empty string is treated as valid — blank rows are filtered out at
 *  submit time by buildAccessListPayload, not flagged as errors while typing. */
function isValidIPOrCIDR(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed) return true;
  const [addr, prefix, ...rest] = trimmed.split("/");
  if (rest.length > 0) return false;
  if (isValidIPv4(addr)) {
    return prefix === undefined || (/^\d{1,2}$/.test(prefix) && Number(prefix) <= 32);
  }
  if (isValidIPv6(addr)) {
    return prefix === undefined || (/^\d{1,3}$/.test(prefix) && Number(prefix) <= 128);
  }
  return false;
}

const protocolOptions: ComboboxOption[] = PROTOCOL_TABLE.map((p) => ({ value: p.id, label: p.displayName }));

const quotaBadgeColors = [
  "bg-indigo-50 text-indigo-700",
  "bg-rose-50 text-rose-700",
  "bg-teal-50 text-teal-700",
  "bg-amber-50 text-amber-700",
  "bg-fuchsia-50 text-fuchsia-700",
];

/** Quota types surfaced as list-card summary badges, in display order. Budget
 *  quotas are intentionally excluded — there's no WebUI editor for them yet. */
const quotaSummaryOrder: { type: string; zh: string; en: string }[] = [
  { type: "requests", zh: "限频", en: "Rate" },
  { type: "tokens", zh: "限量", en: "Tokens" },
  { type: "concurrency", zh: "并发", en: "Concurrency" },
];

function formatQuotaRule(q: ConsumerQuota) {
  return q.window ? `${q.quota_limit}/${q.window}` : `${q.quota_limit}`;
}

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
        <FieldLabel>{title}</FieldLabel>
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
            {rows.length > 1 ? (
              <button
                type="button"
                onClick={() => updateRows(kind, rows.filter((_, i) => i !== idx))}
                className="cursor-pointer p-1 text-slate-400 hover:text-red-500"
                title={isZh ? "删除该条限额" : "Remove rule"}
              >
                <Trash2 className="h-4 w-4" />
              </button>
            ) : (
              <button
                type="button"
                onClick={() => updateRows(kind, [{ limit: "", window: "" }])}
                className="cursor-pointer p-1 text-slate-400 hover:text-slate-600"
                title={isZh ? "清空" : "Clear"}
              >
                <X className="h-4 w-4" />
              </button>
            )}
          </div>
        ))}
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
    );
  }

  return (
    <div className="grid grid-cols-2 items-start gap-4">
      {renderGroup("requests", isZh ? "请求频率限额" : "Rate limit")}
      {renderGroup("tokens", isZh ? "Token 用量限额" : "Token quota")}
      <div className="space-y-2">
        <FieldLabel
          info={
            isZh
              ? "限制同时处理中的最大请求数，留空不做限制"
              : "Caps the number of requests processed at the same time; leave empty for no limit"
          }
        >
          {isZh ? "并发上限" : "Concurrency limit"}
        </FieldLabel>
        <Input
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          value={value.concurrency}
          onChange={(e) => onChange({ ...value, concurrency: digitsOnly(e.target.value) })}
          placeholder={isZh ? "如：10" : "e.g. 10"}
        />
      </div>
    </div>
  );
}

function LimitsEditor({
  value,
  onChange,
  isZh,
}: {
  value: LimitsFormState;
  onChange: (next: LimitsFormState) => void;
  isZh: boolean;
}) {
  return (
    <div className="grid grid-cols-3 gap-4">
      <div className="space-y-2">
        <FieldLabel>{isZh ? "最大输入 Token" : "Max input tokens"}</FieldLabel>
        <Input
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          value={value.maxInputTokens}
          onChange={(e) => onChange({ ...value, maxInputTokens: digitsOnly(e.target.value) })}
          placeholder={isZh ? "如：4000" : "e.g. 4000"}
        />
      </div>
      <div className="space-y-2">
        <FieldLabel>{isZh ? "最大输出 Token" : "Max output tokens"}</FieldLabel>
        <Input
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          value={value.maxOutputTokens}
          onChange={(e) => onChange({ ...value, maxOutputTokens: digitsOnly(e.target.value) })}
          placeholder={isZh ? "如：2000" : "e.g. 2000"}
        />
      </div>
      <div className="space-y-2">
        <FieldLabel>{isZh ? "最大请求体积（字节）" : "Max request body (bytes)"}</FieldLabel>
        <Input
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          value={value.maxRequestBodyBytes}
          onChange={(e) => onChange({ ...value, maxRequestBodyBytes: digitsOnly(e.target.value) })}
          placeholder={isZh ? "如：1048576" : "e.g. 1048576"}
        />
      </div>
    </div>
  );
}

function IPAllowlistEditor({
  value,
  onChange,
  isZh,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  isZh: boolean;
}) {
  function updateRows(rows: string[]) {
    onChange(rows);
  }

  return (
    <div className="space-y-2">
      {value.map((ip, idx) => {
        const invalid = ip.trim() !== "" && !isValidIPOrCIDR(ip);
        return (
          <div key={idx} className="flex items-center gap-2">
            <Input
              value={ip}
              onChange={(e) => {
                const next = value.slice();
                next[idx] = e.target.value;
                updateRows(next);
              }}
              placeholder={isZh ? "例如 10.0.0.0/8 或 192.168.1.1" : "e.g. 10.0.0.0/8 or 192.168.1.1"}
              className={invalid ? "border-red-400 focus-visible:ring-red-400" : ""}
            />
            {value.length > 1 ? (
              <button
                type="button"
                onClick={() => updateRows(value.filter((_, i) => i !== idx))}
                className="cursor-pointer p-1 text-slate-400 hover:text-red-500"
                title={isZh ? "删除该条" : "Remove"}
              >
                <Trash2 className="h-4 w-4" />
              </button>
            ) : (
              <button
                type="button"
                onClick={() => updateRows([""])}
                className="cursor-pointer p-1 text-slate-400 hover:text-slate-600"
                title={isZh ? "清空" : "Clear"}
              >
                <X className="h-4 w-4" />
              </button>
            )}
          </div>
        );
      })}
      <Button type="button" variant="secondary" size="sm" onClick={() => updateRows([...value, ""])}>
        <Plus className="h-3.5 w-3.5" />
        {isZh ? "添加一条" : "Add rule"}
      </Button>
    </div>
  );
}

type CreateForm = {
  name: string;
  routes: string[];
  protocols: string[];
  ipAllowlist: string[];
  quotas: QuotaFormState;
  limits: LimitsFormState;
  keyExpiresPreset: ExpirePreset;
};

const emptyCreate: CreateForm = {
  name: "",
  routes: [],
  protocols: [],
  ipAllowlist: [""],
  quotas: emptyQuotaForm,
  limits: emptyLimitsForm,
  keyExpiresPreset: "never",
};

type EditForm = {
  id: string;
  name: string;
  enabled: boolean;
  routes: string[];
  protocols: string[];
  ipAllowlist: string[];
  quotas: QuotaFormState;
  limits: LimitsFormState;
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

  const [addKeyDialogFor, setAddKeyDialogFor] = useState<Consumer | null>(null);
  const [addKeyForm, setAddKeyForm] = useState<{ name: string; expiresPreset: ExpirePreset }>({
    name: "",
    expiresPreset: "never",
  });

  const [editKeyDialogFor, setEditKeyDialogFor] = useState<{ consumer: Consumer; key: ConsumerKey } | null>(null);
  // expiresTouched tracks whether the user actually picked a validity preset
  // in this dialog session: expires_at is only included in the submitted
  // UpdateConsumerKey when true, so opening the dialog just to rename a key
  // can never silently clear its expiry (nil expires_at means "unchanged").
  const [editKeyForm, setEditKeyForm] = useState<{ name: string; expiresPreset: ExpirePreset; expiresTouched: boolean }>({
    name: "",
    expiresPreset: "never",
    expiresTouched: false,
  });

  const [consumerToDelete, setConsumerToDelete] = useState<Consumer | null>(null);
  const [keyToRegenerate, setKeyToRegenerate] = useState<{ consumer: Consumer; key: ConsumerKey } | null>(null);
  const [keyToDelete, setKeyToDelete] = useState<{ consumer: Consumer; key: ConsumerKey } | null>(null);
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
      setAddKeyDialogFor(null);
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

  const regenerateKeyMut = useMutation({
    mutationFn: async ({ consumerId, key }: { consumerId: string; key: ConsumerKey }) => {
      // consumer_keys has a UNIQUE(consumer_id, name) constraint, so adding the
      // replacement under the *same* name while the old row still exists would
      // always violate it. Create it under a throwaway temp name first (add
      // before delete, so a failed add leaves the old key intact), delete the
      // old key, then rename the replacement back to the original name.
      const tempName = `${key.name}~regen~${crypto.randomUUID()}`;
      const created = await backend<ConsumerKey>("add_consumer_key", {
        id: consumerId,
        input: { name: tempName, expires_at: key.expires_at },
      });
      await backend("delete_consumer_key", { id: consumerId, keyId: key.id });
      await backend<ConsumerKey>("update_consumer_key", {
        id: consumerId,
        keyId: created.id,
        input: { name: key.name },
      });
      return { ...created, name: key.name };
    },
    onSuccess: (created) => {
      invalidateConsumers();
      if (created.token) {
        openRevealDialog({ name: created.name, token: created.token });
      }
    },
    onError: (error: unknown) => {
      showErrorDialog("重新生成 Key 失败", "Failed to regenerate key", error);
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
      protocols: item.protocols ?? [],
      ipAllowlist: ipAllowlistToForm(item.ip_allowlist),
      quotas: quotasToForm(item.quotas),
      limits: limitsToForm(item.limits),
    });
  }

  function openAddKeyDialog(consumer: Consumer) {
    setAddKeyForm({ name: "", expiresPreset: "never" });
    setAddKeyDialogFor(consumer);
  }

  // expiresPreset starts at "never" rather than trying to reverse-map
  // key.expires_at to a preset (presets are relative day-offsets computed at
  // selection time, so an existing absolute timestamp generally can't be
  // mapped back to one) — expiresTouched starts false so this default is
  // never actually submitted unless the user picks a preset themselves.
  function openEditKeyDialog(consumer: Consumer, key: ConsumerKey) {
    setEditKeyForm({ name: key.name, expiresPreset: "never", expiresTouched: false });
    setEditKeyDialogFor({ consumer, key });
  }

  /** Renders one count badge per access dimension (models/protocols/IP
   *  allowlist) that has a restriction set, instead of one badge per bound
   *  item — keeps the card width independent of how many models/protocols/IPs
   *  are configured. A dimension with nothing set means default-allow, so it
   *  renders no badge at all. */
  function renderAccessBadges(consumer: Consumer) {
    const dims: { count: number; zh: string; en: string; className: string }[] = [
      { count: consumer.routes?.length ?? 0, zh: "模型", en: "Models", className: "bg-cyan-50 text-cyan-700" },
      { count: consumer.protocols?.length ?? 0, zh: "协议", en: "Protocols", className: "bg-violet-50 text-violet-700" },
      { count: consumer.ip_allowlist?.length ?? 0, zh: "白名单", en: "IPs", className: "bg-slate-100 text-slate-600" },
    ];
    return dims
      .filter((d) => d.count > 0)
      .map((d) => (
        <Badge key={d.en} variant="warning" className={`connect-label-badge ${d.className}`}>
          {`${isZh ? d.zh : d.en} ${d.count}`}
        </Badge>
      ));
  }

  /** Renders exactly one badge per quota type present, so the card's width
   *  never grows with the number of configured rules — extra rules of the
   *  same type fold into a "+N" suffix on that one badge instead of adding
   *  more badges. */
  function renderQuotaBadges(quotas: ConsumerQuota[] | undefined) {
    return quotaSummaryOrder.flatMap(({ type, zh, en }, idx) => {
      const rows = (quotas ?? []).filter((q) => q.quota_type === type);
      if (rows.length === 0) return [];
      const label = isZh ? zh : en;
      const suffix = rows.length > 1 ? ` +${rows.length - 1}` : "";
      return [
        <Badge
          key={type}
          variant="warning"
          className={`connect-label-badge ${quotaBadgeColors[idx % quotaBadgeColors.length]}`}
        >
          {`${label} ${formatQuotaRule(rows[0])}${suffix}`}
        </Badge>,
      ];
    });
  }

  // Single-key simplification: the backend keeps keys[] as 1:N, but the UI
  // always operates on the consumer's primary key (keys[0]) and never exposes
  // per-key add/delete.
  function renderKeyRow(consumer: Consumer, key: ConsumerKey) {
    const keyExpired = isApiKeyExpired(key.expires_at);
    return (
      <div key={key.id} className="flex items-center justify-between rounded-xl bg-slate-50/60 p-3">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="inline-flex h-5 items-center text-xs text-slate-800">{key.name}</span>
          <code
            title={
              isZh
                ? "完整 Key 仅在创建/新增/重新生成时展示一次，此处仅展示部分字符，无法复制完整 Key"
                : "Full key is shown once on create/add/regenerate; this is a partial preview and cannot be copied here"
            }
            className="inline-flex h-5 items-center rounded bg-slate-100 px-2 py-0.5 text-[10px] leading-none font-medium text-slate-600"
          >
            {formatKeyPreview(key.key_preview)}
          </code>
          {!key.enabled && (
            <Badge variant="danger" className="connect-label-badge">
              {isZh ? "已禁用" : "Disabled"}
            </Badge>
          )}
          <Badge variant={keyExpired ? "danger" : "success"} className="connect-label-badge">
            {formatValidityLabel(keyExpired, isZh)}
          </Badge>
          <span className="text-xs text-slate-500">{formatExpiresText(key.expires_at, isZh)}</span>
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
            onClick={() => openEditKeyDialog(consumer, key)}
            title={isZh ? "编辑名称 / 有效期" : "Edit name / validity"}
            className="cursor-pointer rounded-lg p-1.5 text-slate-400 transition-colors hover:bg-blue-50 hover:text-blue-500"
          >
            <Pencil className="h-4 w-4" />
          </button>
          <button
            onClick={() => setKeyToRegenerate({ consumer, key })}
            title={isZh ? "重新生成 Key（旧 Key 将立即失效）" : "Regenerate key (old key is invalidated immediately)"}
            className="cursor-pointer rounded-lg p-1.5 text-slate-400 transition-colors hover:bg-blue-50 hover:text-blue-500"
          >
            <RotateCw className="h-4 w-4" />
          </button>
          <button
            onClick={() => setKeyToDelete({ consumer, key })}
            title={isZh ? "删除该 Key" : "Delete key"}
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
    if (keys.length === 0) {
      return (
        <div className="flex items-center justify-between rounded-xl bg-slate-50/60 p-3">
          <p className="text-xs text-slate-400">
            {isZh ? "该 consumer 暂无可用密钥，无法鉴权。" : "No key yet — this consumer cannot authenticate."}
          </p>
          <Button type="button" size="sm" variant="secondary" onClick={() => openAddKeyDialog(consumer)}>
            <Plus className="h-3.5 w-3.5" />
            {isZh ? "新增 Key" : "Add key"}
          </Button>
        </div>
      );
    }
    return <div className="space-y-1.5">{keys.map((key) => renderKeyRow(consumer, key))}</div>;
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
              <SectionTitle>{isZh ? "1. 基本信息" : "1. Basic Information"}</SectionTitle>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <FieldLabel required>{isZh ? "名称" : "Name"}</FieldLabel>
                  <Input
                    value={createForm.name}
                    onChange={(e) => setCreateForm((prev) => ({ ...prev, name: e.target.value }))}
                    placeholder={isZh ? "如：生产环境" : "e.g. Production"}
                  />
                </div>
                <div className="space-y-2">
                  <FieldLabel>{isZh ? "Key 有效期" : "Key validity"}</FieldLabel>
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
              <SectionTitle>{isZh ? "2. 访问权限" : "2. Access Permission"}</SectionTitle>
              <div className="grid grid-cols-2 items-start gap-4">
                <div className="space-y-2">
                  <FieldLabel
                    info={
                      isZh
                        ? "不绑定时默认允许访问所有受控模型"
                        : "When left unbound, all protected models are accessible"
                    }
                  >
                    {isZh ? "绑定模型" : "Bind Models"}
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
                <div className="space-y-2">
                  <FieldLabel
                    info={isZh ? "留空时不限制可用协议" : "Leaving this empty applies no protocol restriction"}
                  >
                    {isZh ? "允许协议" : "Allowed protocols"}
                  </FieldLabel>
                  <MultiSelect
                    options={protocolOptions}
                    values={createForm.protocols}
                    placeholder={isZh ? "选择允许的协议" : "Select allowed protocols"}
                    searchPlaceholder={isZh ? "搜索协议..." : "Search protocols..."}
                    emptyText={isZh ? "无匹配协议" : "No matching protocols"}
                    onChange={(next) => setCreateForm((prev) => ({ ...prev, protocols: next }))}
                  />
                </div>
                <div className="space-y-2">
                  <FieldLabel
                    info={isZh ? "留空时不限制来源 IP" : "Leaving this empty applies no restriction on source IP"}
                  >
                    {isZh ? "IP 白名单" : "IP allowlist"}
                  </FieldLabel>
                  <IPAllowlistEditor
                    value={createForm.ipAllowlist}
                    onChange={(next) => setCreateForm((prev) => ({ ...prev, ipAllowlist: next }))}
                    isZh={isZh}
                  />
                </div>
              </div>
            </div>

            <div className="h-px bg-slate-200/70" />

            <div className="space-y-3">
              <SectionTitle>{isZh ? "3. 访问限额" : "3. Access Quota"}</SectionTitle>
              <QuotaEditor
                value={createForm.quotas}
                onChange={(next) => setCreateForm((prev) => ({ ...prev, quotas: next }))}
                isZh={isZh}
                windowOptions={windowOptions}
              />
            </div>

            <div className="h-px bg-slate-200/70" />

            <div className="space-y-3">
              <SectionTitle info={isZh ? "均为可选项，留空表示不做限制" : "All optional; leave empty for no limit"}>{isZh ? "4. 资源限额" : "4. Resource Limits"}</SectionTitle>
              <LimitsEditor
                value={createForm.limits}
                onChange={(next) => setCreateForm((prev) => ({ ...prev, limits: next }))}
                isZh={isZh}
              />
            </div>
          </div>
          <div className="flex gap-3">
            <Button
              onClick={() =>
                createMut.mutate({
                  name: createForm.name.trim(),
                  routes: createForm.routes,
                  protocols: createForm.protocols,
                  ip_allowlist: buildAccessListPayload(createForm.ipAllowlist),
                  quotas: buildQuotasPayload(createForm.quotas),
                  limits: buildLimitsPayload(createForm.limits),
                  keys: [
                    {
                      name: "default",
                      expires_at: resolveExpiresAt(createForm.keyExpiresPreset),
                    },
                  ],
                })
              }
              disabled={
                createMut.isPending ||
                !createForm.name.trim() ||
                createForm.ipAllowlist.some((ip) => !isValidIPOrCIDR(ip))
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
                      <SectionTitle>{isZh ? "1. 基本信息" : "1. Basic Information"}</SectionTitle>
                      <div className="grid grid-cols-2 gap-4">
                        <div className="space-y-2">
                          <FieldLabel required>{isZh ? "名称" : "Name"}</FieldLabel>
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
                            className="flex h-10 w-full cursor-pointer items-center gap-2 rounded-md border border-input px-3 text-sm"
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
                      <SectionTitle>{isZh ? "2. 访问权限" : "2. Access Permission"}</SectionTitle>
                      <div className="grid grid-cols-2 items-start gap-4">
                        <div className="space-y-2">
                          <FieldLabel
                    info={
                      isZh
                        ? "不绑定时默认允许访问所有受控模型"
                        : "When left unbound, all protected models are accessible"
                    }
                  >
                    {isZh ? "绑定模型" : "Bind Models"}
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
                        <div className="space-y-2">
                          <FieldLabel
                            info={isZh ? "留空时不限制可用协议" : "Leaving this empty applies no protocol restriction"}
                          >
                            {isZh ? "允许协议" : "Allowed protocols"}
                          </FieldLabel>
                          <MultiSelect
                            options={protocolOptions}
                            values={editForm.protocols}
                            placeholder={isZh ? "选择允许的协议" : "Select allowed protocols"}
                            searchPlaceholder={isZh ? "搜索协议..." : "Search protocols..."}
                            emptyText={isZh ? "无匹配协议" : "No matching protocols"}
                            onChange={(next) => setEditForm((prev) => (prev ? { ...prev, protocols: next } : prev))}
                          />
                        </div>
                        <div className="space-y-2">
                          <FieldLabel
                            info={isZh ? "留空时不限制来源 IP" : "Leaving this empty applies no restriction on source IP"}
                          >
                            {isZh ? "IP 白名单" : "IP allowlist"}
                          </FieldLabel>
                          <IPAllowlistEditor
                            value={editForm.ipAllowlist}
                            onChange={(next) => setEditForm((prev) => (prev ? { ...prev, ipAllowlist: next } : prev))}
                            isZh={isZh}
                          />
                        </div>
                      </div>
                    </div>

                    <div className="h-px bg-slate-200/70" />

                    <div className="space-y-3">
                      <SectionTitle>{isZh ? "3. 访问限额" : "3. Access Quota"}</SectionTitle>
                      <QuotaEditor
                        value={editForm.quotas}
                        onChange={(next) => setEditForm((prev) => (prev ? { ...prev, quotas: next } : prev))}
                        isZh={isZh}
                        windowOptions={windowOptions}
                      />
                    </div>

                    <div className="h-px bg-slate-200/70" />

                    <div className="space-y-3">
                      <SectionTitle info={isZh ? "均为可选项，留空表示不做限制" : "All optional; leave empty for no limit"}>{isZh ? "4. 资源限额" : "4. Resource Limits"}</SectionTitle>
                      <LimitsEditor
                        value={editForm.limits}
                        onChange={(next) => setEditForm((prev) => (prev ? { ...prev, limits: next } : prev))}
                        isZh={isZh}
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
                            protocols: editForm.protocols,
                            ip_allowlist: buildAccessListPayload(editForm.ipAllowlist),
                            quotas: buildQuotasPayload(editForm.quotas),
                            limits: buildLimitsPayload(editForm.limits),
                          },
                        })
                      }
                      disabled={updateMut.isPending || editForm.ipAllowlist.some((ip) => !isValidIPOrCIDR(ip))}
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
                        {renderAccessBadges(item)}
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
                      onClick={() => openAddKeyDialog(item)}
                      title={isZh ? "新增 Key" : "Add key"}
                      className="cursor-pointer rounded-lg p-2 text-slate-400 transition-colors hover:bg-green-50 hover:text-green-600"
                    >
                      <Plus className="h-4 w-4" />
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
              ? `此操作不可撤销，将同时删除其 Key。确认删除「${consumerToDelete.name}」吗？`
              : `This action cannot be undone and will delete its key. Delete "${consumerToDelete.name}"?`)
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
        open={Boolean(keyToRegenerate)}
        onOpenChange={(open) => {
          if (!open) setKeyToRegenerate(null);
        }}
        title={isZh ? "确认重新生成 Key" : "Confirm key regeneration"}
        description={
          keyToRegenerate
            ? (isZh
              ? `重新生成后旧 Key 将立即失效，新 Key 会以相同名称生成，完整值仅显示一次。确认继续吗？`
              : `Regenerating will immediately invalidate the old key. A new key with the same name will be generated and its full value shown once. Continue?`)
            : undefined
        }
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={isZh ? "重新生成" : "Regenerate"}
        onConfirm={() => {
          if (!keyToRegenerate) return;
          regenerateKeyMut.mutate({ consumerId: keyToRegenerate.consumer.id, key: keyToRegenerate.key });
          setKeyToRegenerate(null);
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

      <Dialog
        open={Boolean(addKeyDialogFor)}
        onOpenChange={(open) => {
          if (!open) setAddKeyDialogFor(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{isZh ? "新增 Key" : "Add Key"}</DialogTitle>
            <DialogDescription>
              {isZh
                ? "Key 值会在创建后自动生成，仅在创建成功后展示一次。"
                : "The key value is auto-generated and shown exactly once after creation."}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-2">
              <FieldLabel required>{isZh ? "名称" : "Name"}</FieldLabel>
              <Input
                value={addKeyForm.name}
                onChange={(e) => setAddKeyForm((prev) => ({ ...prev, name: e.target.value }))}
                placeholder={isZh ? "如：default" : "e.g. default"}
              />
            </div>
            <div className="space-y-2">
              <FieldLabel>{isZh ? "有效期" : "Validity"}</FieldLabel>
              <Select
                value={addKeyForm.expiresPreset}
                onValueChange={(value: ExpirePreset) => setAddKeyForm((prev) => ({ ...prev, expiresPreset: value }))}
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
          <DialogFooter>
            <Button variant="secondary" onClick={() => setAddKeyDialogFor(null)}>
              {isZh ? "取消" : "Cancel"}
            </Button>
            <Button
              disabled={addKeyMut.isPending || !addKeyForm.name.trim()}
              onClick={() => {
                if (!addKeyDialogFor) return;
                addKeyMut.mutate({
                  consumerId: addKeyDialogFor.id,
                  input: {
                    name: addKeyForm.name.trim(),
                    expires_at: resolveExpiresAt(addKeyForm.expiresPreset),
                  },
                });
              }}
            >
              {addKeyMut.isPending ? (isZh ? "添加中..." : "Adding...") : (isZh ? "添加" : "Add")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={Boolean(editKeyDialogFor)}
        onOpenChange={(open) => {
          if (!open) setEditKeyDialogFor(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{isZh ? "编辑 Key" : "Edit Key"}</DialogTitle>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-2">
              <FieldLabel required>{isZh ? "名称" : "Name"}</FieldLabel>
              <Input
                value={editKeyForm.name}
                onChange={(e) => setEditKeyForm((prev) => ({ ...prev, name: e.target.value }))}
              />
            </div>
            <div className="space-y-2">
              <FieldLabel>{isZh ? "有效期" : "Validity"}</FieldLabel>
              <Select
                value={editKeyForm.expiresPreset}
                onValueChange={(value: ExpirePreset) =>
                  setEditKeyForm((prev) => ({ ...prev, expiresPreset: value, expiresTouched: true }))
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
          <DialogFooter>
            <Button variant="secondary" onClick={() => setEditKeyDialogFor(null)}>
              {isZh ? "取消" : "Cancel"}
            </Button>
            <Button
              disabled={updateKeyMut.isPending || !editKeyForm.name.trim()}
              onClick={() => {
                if (!editKeyDialogFor) return;
                const input: UpdateConsumerKey = { name: editKeyForm.name.trim() };
                if (editKeyForm.expiresTouched) {
                  input.expires_at = resolveExpiresAtForUpdate(editKeyForm.expiresPreset);
                }
                updateKeyMut.mutate({
                  consumerId: editKeyDialogFor.consumer.id,
                  keyId: editKeyDialogFor.key.id,
                  input,
                });
                setEditKeyDialogFor(null);
              }}
            >
              {updateKeyMut.isPending ? (isZh ? "保存中..." : "Saving...") : (isZh ? "保存" : "Save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

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
