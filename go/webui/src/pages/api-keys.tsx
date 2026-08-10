/* eslint-disable react-hooks/set-state-in-effect */

import { useCallback, useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useLocation, useNavigate } from "react-router-dom";
import { formatKeyPreview } from "@/lib/format";
import {
  Check,
  ChevronLeft,
  ChevronRight,
  Copy,
  Info,
  Pencil,
  Plus,
  RotateCw,
  Search,
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
import { DataTable, type DataTableColumn } from "@/components/v2/data-table";
import { EmptyState } from "@/components/v2/empty-state";
import { FilterBar } from "@/components/v2/filter-bar";
import { MetricLedger } from "@/components/v2/metric-ledger";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { ResourceEditorDialog } from "@/components/v2/resource-editor-dialog";
import { Status } from "@/components/v2/status";
import { Surface } from "@/components/v2/surface";
import { filterConsumers, type ConsumerFilters } from "@/features/consumers/consumer-view-model";
import { localizedMessage, type MessageKey } from "@/lib/messages";

const PAGE_SIZE = 7;

// CopyFullKeyButton copies a recoverable raw key (present only when the admin
// runs with --plaintext-keys) to the clipboard, briefly swapping its icon to a
// check as feedback. Module-scope so it can own per-row state without breaking
// the rules-of-hooks in the renderKeyRow map callback.
function CopyFullKeyButton({ token, isZh }: { token: string; isZh: boolean }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      title={copied ? (localizedMessage(isZh, "v2.api-keys.copied")) : localizedMessage(isZh, "v2.api-keys.copyFullKey")}
      aria-label={localizedMessage(isZh, "v2.api-keys.copyFullKey")}
      className="inline-flex text-slate-400 transition-colors hover:text-slate-600"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(token);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          // Clipboard may be unavailable (insecure context); ignore silently.
        }
      }}
    >
      {copied ? <Check className="h-3.5 w-3.5 text-emerald-600" /> : <Copy className="h-3.5 w-3.5" />}
    </button>
  );
}

type ExpirePreset = "never" | "1d" | "7d" | "30d" | "90d" | "180d" | "1y";

const expirePresetOptions: { value: ExpirePreset; label: MessageKey }[] = [
  { value: "never", label: "consumers.expiry.never" },
  { value: "1d", label: "consumers.expiry.1d" },
  { value: "7d", label: "consumers.expiry.7d" },
  { value: "30d", label: "consumers.expiry.30d" },
  { value: "90d", label: "consumers.expiry.90d" },
  { value: "180d", label: "consumers.expiry.180d" },
  { value: "1y", label: "consumers.expiry.1y" },
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


function formatExpiresText(value: string | null | undefined, isZh: boolean) {
  if (!value) return localizedMessage(isZh, "v2.api-keys.never");
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
  return expired ? (localizedMessage(isZh, "v2.api-keys.expired")) : (localizedMessage(isZh, "v2.api-keys.valid"));
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
          <p className="text-xs text-slate-400">{localizedMessage(isZh, "v2.api-keys.notSetUnlimited")}</p>
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
              placeholder={localizedMessage(isZh, "v2.api-keys.limit")}
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
                placeholder={localizedMessage(isZh, "v2.api-keys.window")}
              />
            </div>
            {rows.length > 1 ? (
              <button
                type="button"
                onClick={() => updateRows(kind, rows.filter((_, i) => i !== idx))}
                className="cursor-pointer p-1 text-slate-400 hover:text-red-500"
                title={localizedMessage(isZh, "v2.api-keys.removeRule")}
              >
                <Trash2 className="h-4 w-4" />
              </button>
            ) : (
              <button
                type="button"
                onClick={() => updateRows(kind, [{ limit: "", window: "" }])}
                className="cursor-pointer p-1 text-slate-400 hover:text-slate-600"
                title={localizedMessage(isZh, "v2.api-keys.clear")}
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
          {localizedMessage(isZh, "v2.api-keys.addRule")}
        </Button>
      </div>
    );
  }

  return (
    <div className="grid grid-cols-2 items-start gap-4">
      {renderGroup("requests", localizedMessage(isZh, "v2.api-keys.rateLimit"))}
      {renderGroup("tokens", localizedMessage(isZh, "v2.api-keys.tokenQuota"))}
      <div className="space-y-2">
        <FieldLabel
          info={
            localizedMessage(isZh, "v2.api-keys.capsTheNumberOfRequestsProcessedAtThe")
          }
        >
          {localizedMessage(isZh, "v2.api-keys.concurrencyLimit")}
        </FieldLabel>
        <Input
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          value={value.concurrency}
          onChange={(e) => onChange({ ...value, concurrency: digitsOnly(e.target.value) })}
          placeholder={localizedMessage(isZh, "v2.api-keys.eG10")}
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
        <FieldLabel>{localizedMessage(isZh, "v2.api-keys.maxInputTokens")}</FieldLabel>
        <Input
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          value={value.maxInputTokens}
          onChange={(e) => onChange({ ...value, maxInputTokens: digitsOnly(e.target.value) })}
          placeholder={localizedMessage(isZh, "v2.api-keys.eG4000")}
        />
      </div>
      <div className="space-y-2">
        <FieldLabel>{localizedMessage(isZh, "v2.api-keys.maxOutputTokens")}</FieldLabel>
        <Input
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          value={value.maxOutputTokens}
          onChange={(e) => onChange({ ...value, maxOutputTokens: digitsOnly(e.target.value) })}
          placeholder={localizedMessage(isZh, "v2.api-keys.eG2000")}
        />
      </div>
      <div className="space-y-2">
        <FieldLabel>{localizedMessage(isZh, "v2.api-keys.maxRequestBodyBytes")}</FieldLabel>
        <Input
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          value={value.maxRequestBodyBytes}
          onChange={(e) => onChange({ ...value, maxRequestBodyBytes: digitsOnly(e.target.value) })}
          placeholder={localizedMessage(isZh, "v2.api-keys.eG1048576")}
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
              placeholder={localizedMessage(isZh, "v2.api-keys.eG100008Or")}
              className={invalid ? "border-red-400 focus-visible:ring-red-400" : ""}
            />
            {value.length > 1 ? (
              <button
                type="button"
                onClick={() => updateRows(value.filter((_, i) => i !== idx))}
                className="cursor-pointer p-1 text-slate-400 hover:text-red-500"
                title={localizedMessage(isZh, "v2.api-keys.remove")}
              >
                <Trash2 className="h-4 w-4" />
              </button>
            ) : (
              <button
                type="button"
                onClick={() => updateRows([""])}
                className="cursor-pointer p-1 text-slate-400 hover:text-slate-600"
                title={localizedMessage(isZh, "v2.api-keys.clear")}
              >
                <X className="h-4 w-4" />
              </button>
            )}
          </div>
        );
      })}
      <Button type="button" variant="secondary" size="sm" onClick={() => updateRows([...value, ""])}>
        <Plus className="h-3.5 w-3.5" />
        {localizedMessage(isZh, "v2.api-keys.addRule")}
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
  const { locale, t } = useLocale();
  const isZh = locale === "zh-CN";
  const qc = useQueryClient();
  const location = useLocation();
  const navigate = useNavigate();

  const windowOptions = useMemo<ComboboxOption[]>(
    () => [
      { value: "", label: localizedMessage(isZh, "v2.api-keys.noWindow") },
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
  const [filters, setFilters] = useState<ConsumerFilters>({ query: "", status: "all" });

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

  function showErrorDialog(titleKey: MessageKey, error: unknown) {
    setErrorDialog({
      title: localizedMessage(isZh, titleKey),
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
      showErrorDialog("consumers.error.create", error);
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
      showErrorDialog("consumers.error.save", error);
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => backend("delete_consumer", { id }),
    onSuccess: () => invalidateConsumers(),
    onError: (error: unknown) => {
      showErrorDialog("consumers.error.delete", error);
    },
  });

  const toggleConsumerEnabledMut = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      backend("update_consumer", { id, input: { enabled } }),
    onSuccess: () => invalidateConsumers(),
    onError: (error: unknown) => {
      showErrorDialog("consumers.error.operation", error);
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
      showErrorDialog("consumers.error.addKey", error);
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
      showErrorDialog("consumers.error.updateKey", error);
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
      showErrorDialog("consumers.error.regenerateKey", error);
    },
  });

  const deleteKeyMut = useMutation({
    mutationFn: ({ consumerId, keyId }: { consumerId: string; keyId: string }) =>
      backend("delete_consumer_key", { id: consumerId, keyId }),
    onSuccess: () => invalidateConsumers(),
    onError: (error: unknown) => {
      showErrorDialog("consumers.error.deleteKey", error);
    },
  });

  const filteredConsumers = useMemo(() => filterConsumers(consumers, filters), [consumers, filters]);
  const totalPages = Math.max(1, Math.ceil(filteredConsumers.length / PAGE_SIZE));
  const pagedConsumers = filteredConsumers.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);

  useEffect(() => {
    if (page > totalPages - 1) setPage(0);
  }, [page, totalPages]);

  useEffect(() => {
    setPage(0);
  }, [filters]);

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

  const startEdit = useCallback((item: Consumer) => {
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
  }, []);

  useEffect(() => {
    const params = new URLSearchParams(location.search);
    if (params.get("action") === "create") {
      setEditingId(null);
      setShowForm(true);
      navigate(location.pathname, { replace: true });
      return;
    }
    const focus = params.get("focus");
    if (!focus) return;
    const consumer = consumers.find((item) => item.id === focus);
    if (!consumer) return;
    setPage(Math.floor(consumers.findIndex((item) => item.id === focus) / PAGE_SIZE));
    startEdit(consumer);
    navigate(location.pathname, { replace: true });
  }, [consumers, location.pathname, location.search, navigate, startEdit]);

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

  // Single-key simplification: the backend keeps keys[] as 1:N, but the UI
  // always operates on the consumer's primary key (keys[0]) and never exposes
  // per-key add/delete.
  function renderKeyRow(consumer: Consumer, key: ConsumerKey) {
    const keyExpired = isApiKeyExpired(key.expires_at);
    // key.token is only present on read when admin runs with --plaintext-keys
    // (recoverable storage); otherwise the full key is shown once at creation.
    const recoverableToken = key.token;
    return (
      <div key={key.id} className="flex items-center justify-between rounded-xl bg-slate-50/60 p-3">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="inline-flex h-5 items-center text-xs text-slate-800">{key.name}</span>
          <code
            title={
              recoverableToken
                ? (localizedMessage(isZh, "v2.api-keys.plaintextStorageIsOnTheFullKeyCan"))
                : localizedMessage(isZh, "v2.api-keys.fullKeyIsShownOnceOnCreateAdd")
            }
            className="inline-flex h-5 items-center rounded bg-slate-100 px-2 py-0.5 text-[10px] leading-none font-medium text-slate-600"
          >
            {formatKeyPreview(key.key_preview)}
          </code>
          {recoverableToken && <CopyFullKeyButton token={recoverableToken} isZh={isZh} />}
          {!key.enabled && (
            <Badge variant="danger" className="connect-label-badge">
              {localizedMessage(isZh, "v2.providers.disabled2")}
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
            title={key.enabled ? (localizedMessage(isZh, "v2.providers.disable")) : (localizedMessage(isZh, "v2.providers.enable"))}
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
            title={localizedMessage(isZh, "v2.api-keys.editNameValidity")}
            className="cursor-pointer rounded-lg p-1.5 text-slate-400 transition-colors hover:bg-blue-50 hover:text-blue-500"
          >
            <Pencil className="h-4 w-4" />
          </button>
          <button
            onClick={() => setKeyToRegenerate({ consumer, key })}
            title={localizedMessage(isZh, "v2.api-keys.regenerateKeyOldKeyIsInvalidatedImmediately")}
            className="cursor-pointer rounded-lg p-1.5 text-slate-400 transition-colors hover:bg-blue-50 hover:text-blue-500"
          >
            <RotateCw className="h-4 w-4" />
          </button>
          <button
            onClick={() => setKeyToDelete({ consumer, key })}
            title={localizedMessage(isZh, "v2.api-keys.deleteKey")}
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
            {localizedMessage(isZh, "v2.api-keys.noKeyYetCannotAuthenticate")}
          </p>
          <Button type="button" size="sm" variant="secondary" onClick={() => openAddKeyDialog(consumer)}>
            <Plus className="h-3.5 w-3.5" />
            {localizedMessage(isZh, "v2.api-keys.addKey")}
          </Button>
        </div>
      );
    }
    return <div className="space-y-1.5">{keys.map((key) => renderKeyRow(consumer, key))}</div>;
  }

  const keyCount = consumers.reduce((sum, consumer) => sum + (consumer.keys?.length ?? 0), 0);
  const activeKeyCount = consumers.reduce(
    (sum, consumer) => sum + (consumer.keys ?? []).filter((key) => key.enabled && !isApiKeyExpired(key.expires_at)).length,
    0,
  );
  const expiringKeyCount = consumers.reduce((sum, consumer) => sum + (consumer.keys ?? []).filter((key) => {
    if (!key.expires_at || isApiKeyExpired(key.expires_at)) return false;
    const expires = Date.parse(key.expires_at.includes("T") ? key.expires_at : `${key.expires_at.replace(" ", "T")}Z`);
    return Number.isFinite(expires) && expires - Date.now() <= 10 * 24 * 60 * 60 * 1000;
  }).length, 0);

  const consumerColumns: DataTableColumn<Consumer>[] = [
    {
      key: "consumer",
      header: localizedMessage(isZh, "v2.api-keys.consumer"),
      render: (consumer) => (
        <div className="v2-consumer-cell">
          <span>{consumer.name.slice(0, 2).toLocaleUpperCase()}</span>
          <div><strong>{consumer.name}</strong><code>{consumer.id}</code></div>
        </div>
      ),
    },
    {
      key: "keys",
      header: "Key",
      render: (consumer) => {
        const keys = consumer.keys ?? [];
        const invalid = keys.filter((key) => !key.enabled || isApiKeyExpired(key.expires_at)).length;
        return <div className="v2-key-count"><strong>{keys.length}</strong><span>{invalid ? localizedMessage(isZh, "common.unavailableCount", { count: invalid }) : localizedMessage(isZh, "v2.api-keys.allValid")}</span></div>;
      },
    },
    {
      key: "routes",
      header: localizedMessage(isZh, "v2.api-keys.modelAccess"),
      render: (consumer) => consumer.routes?.length
        ? <div className="v2-tag-list">{consumer.routes.slice(0, 2).map((route) => <code key={route}>{route}</code>)}{consumer.routes.length > 2 && <span>+{consumer.routes.length - 2}</span>}</div>
        : <span className="v2-unrestricted">{localizedMessage(isZh, "v2.api-keys.allModels")}</span>,
    },
    {
      key: "restrictions",
      header: localizedMessage(isZh, "v2.api-keys.restrictions"),
      render: (consumer) => (
        <div className="v2-access-cell">
          <strong>{consumer.protocols?.length ? consumer.protocols.join(" / ") : (localizedMessage(isZh, "v2.providers.allProtocols"))}</strong>
          <span>{consumer.ip_allowlist?.length ? localizedMessage(isZh, "consumers.ipRules", { count: consumer.ip_allowlist.length }) : localizedMessage(isZh, "v2.api-keys.anySourceIp")}</span>
        </div>
      ),
    },
    {
      key: "quotas",
      header: localizedMessage(isZh, "v2.api-keys.quotas"),
      render: (consumer) => consumer.quotas?.length
        ? <div className="v2-access-cell"><strong>{formatQuotaRule(consumer.quotas[0])}</strong><span>{consumer.quotas.length > 1 ? localizedMessage(isZh, "common.additionalRules", { count: consumer.quotas.length - 1 }) : localizedMessage(isZh, "v2.api-keys.1Rule")}</span></div>
        : <span className="v2-unrestricted">{localizedMessage(isZh, "v2.api-keys.unlimited")}</span>,
    },
    {
      key: "status",
      header: localizedMessage(isZh, "v2.providers.status"),
      render: (consumer) => <Status tone={consumer.enabled ? "success" : "neutral"}>{consumer.enabled ? (localizedMessage(isZh, "v2.api-keys.enabled")) : (localizedMessage(isZh, "v2.api-keys.disabled"))}</Status>,
    },
    {
      key: "actions",
      header: <span className="sr-only">{localizedMessage(isZh, "v2.providers.actions")}</span>,
      className: "v2-table-actions v2-consumer-actions",
      render: (consumer) => (
        <div className="v2-row-actions">
          <button type="button" onClick={(event) => { event.stopPropagation(); toggleConsumerEnabledMut.mutate({ id: consumer.id, enabled: !consumer.enabled }); }} title={consumer.enabled ? (localizedMessage(isZh, "v2.api-keys.disable")) : (localizedMessage(isZh, "v2.providers.enable"))}>{consumer.enabled ? <ToggleRight /> : <ToggleLeft />}</button>
          <button type="button" onClick={(event) => { event.stopPropagation(); openAddKeyDialog(consumer); }} title={localizedMessage(isZh, "v2.api-keys.addKey")}><Plus /></button>
          <button type="button" onClick={(event) => { event.stopPropagation(); startEdit(consumer); }} title={localizedMessage(isZh, "v2.providers.edit")}><Pencil /></button>
          <button type="button" onClick={(event) => { event.stopPropagation(); setConsumerToDelete(consumer); }} title={localizedMessage(isZh, "v2.providers.delete")}><Trash2 /></button>
        </div>
      ),
    },
  ];

  return (
    <PageLayout
      header={(
        <PageHeader
          title={t("page.apiKeys.title")}
          description={t("page.apiKeys.subtitle")}
          actions={<Button onClick={() => { setEditingId(null); setEditForm(null); setShowForm(true); }}><Plus />{localizedMessage(isZh, "v2.api-keys.addConsumer")}</Button>}
        />
      )}
    >

      <MetricLedger items={[
        { key: "consumers", label: localizedMessage(isZh, "v2.api-keys.consumers"), value: consumers.length, detail: localizedMessage(isZh, "common.enabledCount", { count: consumers.filter((item) => item.enabled).length }) },
        { key: "active-keys", label: localizedMessage(isZh, "v2.api-keys.activeKeys"), value: activeKeyCount, detail: localizedMessage(isZh, "consumers.keysTotal", { count: keyCount }), tone: "success" },
        { key: "expiring", label: localizedMessage(isZh, "v2.api-keys.expiringSoon"), value: expiringKeyCount, detail: localizedMessage(isZh, "v2.api-keys.within10Days"), tone: expiringKeyCount ? "warning" : "default" },
        { key: "restricted", label: localizedMessage(isZh, "v2.api-keys.withQuotas"), value: consumers.filter((item) => item.quotas?.length).length, detail: localizedMessage(isZh, "v2.api-keys.consumerLevelLimits") },
      ]} />

      <ResourceEditorDialog
        open={showForm}
        title={localizedMessage(isZh, "v2.api-keys.addConsumer2")}
        description={localizedMessage(isZh, "v2.api-keys.createAnIdentityWithModelAccessRestrictionsAnd")}
        onClose={() => { setShowForm(false); setCreateForm(emptyCreate); }}
        footer={(
          <div className="v2-inspector-footer-actions">
            <Button
              variant="secondary"
              onClick={() => {
                setShowForm(false);
                setCreateForm(emptyCreate);
              }}
            >
              {localizedMessage(isZh, "v2.providers.cancel")}
            </Button>
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
              {createMut.isPending ? (localizedMessage(isZh, "v2.providers.creating")) : (localizedMessage(isZh, "v2.api-keys.create"))}
            </Button>
          </div>
        )}
      >
        <div className="v2-consumer-form">
          <div className="space-y-5">
            <div className="space-y-3">
              <SectionTitle>{localizedMessage(isZh, "v2.api-keys.1BasicInformation")}</SectionTitle>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <FieldLabel required>{localizedMessage(isZh, "v2.providers.name")}</FieldLabel>
                  <Input
                    value={createForm.name}
                    onChange={(e) => setCreateForm((prev) => ({ ...prev, name: e.target.value }))}
                    placeholder={localizedMessage(isZh, "v2.api-keys.eGProduction")}
                  />
                </div>
                <div className="space-y-2">
                  <FieldLabel>{localizedMessage(isZh, "v2.api-keys.keyValidity")}</FieldLabel>
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
                          {localizedMessage(isZh, option.label)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <p className="text-xs text-slate-500">
                {localizedMessage(isZh, "v2.api-keys.theKeyValueIsAutoGeneratedAndShown")}
              </p>
            </div>

            <div className="h-px bg-slate-200/70" />

            <div className="space-y-3">
              <SectionTitle>{localizedMessage(isZh, "v2.api-keys.2AccessPermission")}</SectionTitle>
              <div className="grid grid-cols-2 items-start gap-4">
                <div className="space-y-2">
                  <FieldLabel
                    info={
                      localizedMessage(isZh, "v2.api-keys.whenLeftUnboundAllProtectedModelsAreAccessible")
                    }
                  >
                    {localizedMessage(isZh, "v2.api-keys.bindModels")}
                  </FieldLabel>
                  <MultiSelect
                    options={routeOptions}
                    values={createForm.routes}
                    placeholder={
                      localizedMessage(isZh, "v2.api-keys.selectProtectedModelsThisKeyCanAccess")
                    }
                    searchPlaceholder={localizedMessage(isZh, "v2.api-keys.searchModels")}
                    emptyText={localizedMessage(isZh, "v2.api-keys.noMatchingModels")}
                    onChange={(next) => setCreateForm((prev) => ({ ...prev, routes: next }))}
                  />
                </div>
                <div className="space-y-2">
                  <FieldLabel
                    info={localizedMessage(isZh, "v2.api-keys.leavingThisEmptyAppliesNoProtocolRestriction")}
                  >
                    {localizedMessage(isZh, "v2.api-keys.allowedProtocols")}
                  </FieldLabel>
                  <MultiSelect
                    options={protocolOptions}
                    values={createForm.protocols}
                    placeholder={localizedMessage(isZh, "v2.api-keys.selectAllowedProtocols")}
                    searchPlaceholder={localizedMessage(isZh, "v2.api-keys.searchProtocols")}
                    emptyText={localizedMessage(isZh, "v2.api-keys.noMatchingProtocols")}
                    onChange={(next) => setCreateForm((prev) => ({ ...prev, protocols: next }))}
                  />
                </div>
                <div className="space-y-2">
                  <FieldLabel
                    info={localizedMessage(isZh, "v2.api-keys.leavingThisEmptyAppliesNoRestrictionOnSource")}
                  >
                    {localizedMessage(isZh, "v2.api-keys.ipAllowlist")}
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
              <SectionTitle>{localizedMessage(isZh, "v2.api-keys.3AccessQuota")}</SectionTitle>
              <QuotaEditor
                value={createForm.quotas}
                onChange={(next) => setCreateForm((prev) => ({ ...prev, quotas: next }))}
                isZh={isZh}
                windowOptions={windowOptions}
              />
            </div>

            <div className="h-px bg-slate-200/70" />

            <div className="space-y-3">
              <SectionTitle info={localizedMessage(isZh, "v2.api-keys.allOptionalLeaveEmptyForNoLimit")}>{localizedMessage(isZh, "v2.api-keys.4ResourceLimits")}</SectionTitle>
              <LimitsEditor
                value={createForm.limits}
                onChange={(next) => setCreateForm((prev) => ({ ...prev, limits: next }))}
                isZh={isZh}
              />
            </div>
          </div>
        </div>
      </ResourceEditorDialog>

      <FilterBar summary={localizedMessage(isZh, "common.showing", { visible: filteredConsumers.length, total: consumers.length })}>
        <label className="v2-search-field"><Search /><Input aria-label={localizedMessage(isZh, "v2.api-keys.searchConsumers")} placeholder={localizedMessage(isZh, "v2.api-keys.searchConsumerModelOrProtocol")} value={filters.query} onChange={(event) => setFilters((current) => ({ ...current, query: event.target.value }))} /></label>
        <Select value={filters.status} onValueChange={(status) => setFilters((current) => ({ ...current, status: status as ConsumerFilters["status"] }))}>
          <SelectTrigger aria-label={localizedMessage(isZh, "v2.providers.filterByStatus")}><SelectValue /></SelectTrigger>
          <SelectContent><SelectItem value="all">{localizedMessage(isZh, "v2.providers.allStatuses")}</SelectItem><SelectItem value="enabled">{localizedMessage(isZh, "v2.api-keys.enabled")}</SelectItem><SelectItem value="disabled">{localizedMessage(isZh, "v2.api-keys.disabled")}</SelectItem></SelectContent>
        </Select>
      </FilterBar>

      <Surface className="v2-table-surface" title={localizedMessage(isZh, "v2.api-keys.consumersAndKeys")} description={localizedMessage(isZh, "v2.api-keys.manageIdentityPermissionsAndQuotasAtTheConsumer")}>
        <DataTable
          columns={consumerColumns}
          rows={pagedConsumers}
          rowKey={(consumer) => consumer.id}
          loading={isLoading}
          onRowClick={startEdit}
          empty={<EmptyState title={consumers.length ? (localizedMessage(isZh, "v2.api-keys.noMatchingConsumers")) : (localizedMessage(isZh, "v2.api-keys.noConsumersYet"))} description={consumers.length ? (localizedMessage(isZh, "v2.api-keys.adjustTheSearchOrStatusFilter")) : (localizedMessage(isZh, "v2.api-keys.createAConsumerToGenerateItsFirstApi"))} action={!consumers.length ? <Button onClick={() => setShowForm(true)}><Plus />{localizedMessage(isZh, "v2.api-keys.addConsumer")}</Button> : undefined} />}
        />
        {filteredConsumers.length > PAGE_SIZE && <div className="v2-pagination"><span>{localizedMessage(isZh, "common.pagination", { page: page + 1, total: totalPages })}</span><div><Button variant="outline" size="icon" disabled={page === 0} onClick={() => setPage((current) => current - 1)}><ChevronLeft /></Button><Button variant="outline" size="icon" disabled={page >= totalPages - 1} onClick={() => setPage((current) => current + 1)}><ChevronRight /></Button></div></div>}
      </Surface>

          {pagedConsumers.map((item) => {
            const isEditing = editingId === item.id && editForm;

            if (isEditing && editForm) {
              return (
                <ResourceEditorDialog
                  key={item.id}
                  open
                  title={localizedMessage(isZh, "consumers.edit")}
                  description={item.name}
                  onClose={() => {
                    setEditingId(null);
                    setEditForm(null);
                  }}
                  footer={(
                    <div className="v2-inspector-footer-actions">
                      <Button
                        variant="secondary"
                        onClick={() => {
                          setEditingId(null);
                          setEditForm(null);
                        }}
                      >
                        {localizedMessage(isZh, "v2.providers.cancel")}
                      </Button>
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
                        {updateMut.isPending ? (localizedMessage(isZh, "v2.providers.saving")) : (localizedMessage(isZh, "v2.api-keys.save"))}
                      </Button>
                    </div>
                  )}
                >
                  <div className="v2-consumer-form">
                    <div className="space-y-5">
                      <div className="space-y-3">
                      <SectionTitle>{localizedMessage(isZh, "v2.api-keys.1BasicInformation")}</SectionTitle>
                      <div className="grid grid-cols-2 gap-4">
                        <div className="space-y-2">
                          <FieldLabel required>{localizedMessage(isZh, "v2.providers.name")}</FieldLabel>
                          <Input
                            value={editForm.name}
                            onChange={(e) => setEditForm((prev) => (prev ? { ...prev, name: e.target.value } : prev))}
                          />
                        </div>
                        <div className="space-y-2">
                          <FieldLabel>{localizedMessage(isZh, "v2.providers.status")}</FieldLabel>
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
                            {editForm.enabled ? (localizedMessage(isZh, "v2.providers.enabled")) : (localizedMessage(isZh, "v2.providers.disabled2"))}
                          </button>
                        </div>
                      </div>
                    </div>

                      <div className="h-px bg-slate-200/70" />

                      <div className="space-y-3">
                      <SectionTitle>{localizedMessage(isZh, "v2.api-keys.2AccessPermission")}</SectionTitle>
                      <div className="grid grid-cols-2 items-start gap-4">
                        <div className="space-y-2">
                          <FieldLabel
                    info={
                      localizedMessage(isZh, "v2.api-keys.whenLeftUnboundAllProtectedModelsAreAccessible")
                    }
                  >
                    {localizedMessage(isZh, "v2.api-keys.bindModels")}
                  </FieldLabel>
                          <MultiSelect
                            options={routeOptions}
                            values={editForm.routes}
                            placeholder={
                              localizedMessage(isZh, "v2.api-keys.selectProtectedModelsThisKeyCanAccess")
                            }
                            searchPlaceholder={localizedMessage(isZh, "v2.api-keys.searchModels")}
                            emptyText={localizedMessage(isZh, "v2.api-keys.noMatchingModels")}
                            onChange={(next) => setEditForm((prev) => (prev ? { ...prev, routes: next } : prev))}
                          />
                        </div>
                        <div className="space-y-2">
                          <FieldLabel
                            info={localizedMessage(isZh, "v2.api-keys.leavingThisEmptyAppliesNoProtocolRestriction")}
                          >
                            {localizedMessage(isZh, "v2.api-keys.allowedProtocols")}
                          </FieldLabel>
                          <MultiSelect
                            options={protocolOptions}
                            values={editForm.protocols}
                            placeholder={localizedMessage(isZh, "v2.api-keys.selectAllowedProtocols")}
                            searchPlaceholder={localizedMessage(isZh, "v2.api-keys.searchProtocols")}
                            emptyText={localizedMessage(isZh, "v2.api-keys.noMatchingProtocols")}
                            onChange={(next) => setEditForm((prev) => (prev ? { ...prev, protocols: next } : prev))}
                          />
                        </div>
                        <div className="space-y-2">
                          <FieldLabel
                            info={localizedMessage(isZh, "v2.api-keys.leavingThisEmptyAppliesNoRestrictionOnSource")}
                          >
                            {localizedMessage(isZh, "v2.api-keys.ipAllowlist")}
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
                      <SectionTitle>{localizedMessage(isZh, "v2.api-keys.3AccessQuota")}</SectionTitle>
                      <QuotaEditor
                        value={editForm.quotas}
                        onChange={(next) => setEditForm((prev) => (prev ? { ...prev, quotas: next } : prev))}
                        isZh={isZh}
                        windowOptions={windowOptions}
                      />
                    </div>

                      <div className="h-px bg-slate-200/70" />

                      <div className="space-y-3">
                      <SectionTitle info={localizedMessage(isZh, "v2.api-keys.allOptionalLeaveEmptyForNoLimit")}>{localizedMessage(isZh, "v2.api-keys.4ResourceLimits")}</SectionTitle>
                      <LimitsEditor
                        value={editForm.limits}
                        onChange={(next) => setEditForm((prev) => (prev ? { ...prev, limits: next } : prev))}
                        isZh={isZh}
                      />
                      </div>
                    </div>
                    <div className="h-px bg-slate-200/70" />
                    {renderKeysSection(item)}
                  </div>
                </ResourceEditorDialog>
              );
            }

            return null;
          })}

      <Dialog
        open={showRevealDialog}
        onOpenChange={(open) => {
          setShowRevealDialog(open);
          if (!open) setCopiedRevealKey(false);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{localizedMessage(isZh, "v2.api-keys.keyGenerated")}</DialogTitle>
            <DialogDescription>
              {localizedMessage(isZh, "consumers.revealOnce", { name: revealedKey?.name ?? "" })}
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
              {localizedMessage(isZh, "v2.providers.close")}
            </Button>
            <Button
              onClick={async () => {
                if (!revealedKey?.token) return;
                await navigator.clipboard.writeText(revealedKey.token);
                setCopiedRevealKey(true);
              }}
            >
              {copiedRevealKey ? (localizedMessage(isZh, "v2.api-keys.copied")) : (localizedMessage(isZh, "v2.api-keys.copy"))}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={Boolean(consumerToDelete)}
        onOpenChange={(open) => {
          if (!open) setConsumerToDelete(null);
        }}
        title={localizedMessage(isZh, "v2.api-keys.confirmKeyDeletion")}
        description={
          consumerToDelete
            ? localizedMessage(isZh, "consumers.deleteDescription", { name: consumerToDelete.name })
            : undefined
        }
        cancelText={localizedMessage(isZh, "v2.providers.cancel")}
        confirmText={localizedMessage(isZh, "v2.providers.delete")}
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
        title={localizedMessage(isZh, "v2.api-keys.confirmKeyRegeneration")}
        description={
          keyToRegenerate
            ? (localizedMessage(isZh, "v2.api-keys.regeneratingWillImmediatelyInvalidateTheOldKeyA"))
            : undefined
        }
        cancelText={localizedMessage(isZh, "v2.providers.cancel")}
        confirmText={localizedMessage(isZh, "v2.api-keys.regenerate")}
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
        title={localizedMessage(isZh, "v2.api-keys.confirmKeyDeletion2")}
        description={
          keyToDelete
            ? ((keyToDelete.consumer.keys?.length ?? 0) <= 1
              ? localizedMessage(isZh, "consumers.deleteOnlyKeyDescription", {
                  key: keyToDelete.key.name,
                  consumer: keyToDelete.consumer.name,
                })
              : localizedMessage(isZh, "consumers.deleteKeyDescription", { name: keyToDelete.key.name }))
            : undefined
        }
        cancelText={localizedMessage(isZh, "v2.providers.cancel")}
        confirmText={localizedMessage(isZh, "v2.providers.delete")}
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
            <DialogTitle>{localizedMessage(isZh, "v2.api-keys.addKey2")}</DialogTitle>
            <DialogDescription>
              {localizedMessage(isZh, "v2.api-keys.theKeyValueIsAutoGeneratedAndShown")}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-2">
              <FieldLabel required>{localizedMessage(isZh, "v2.providers.name")}</FieldLabel>
              <Input
                value={addKeyForm.name}
                onChange={(e) => setAddKeyForm((prev) => ({ ...prev, name: e.target.value }))}
                placeholder={localizedMessage(isZh, "v2.api-keys.eGDefault")}
              />
            </div>
            <div className="space-y-2">
              <FieldLabel>{localizedMessage(isZh, "v2.api-keys.validity")}</FieldLabel>
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
                      {localizedMessage(isZh, option.label)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="secondary" onClick={() => setAddKeyDialogFor(null)}>
              {localizedMessage(isZh, "v2.providers.cancel")}
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
              {addKeyMut.isPending ? (localizedMessage(isZh, "v2.api-keys.adding")) : (localizedMessage(isZh, "v2.api-keys.add"))}
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
            <DialogTitle>{localizedMessage(isZh, "v2.api-keys.editKey2")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-2">
              <FieldLabel required>{localizedMessage(isZh, "v2.providers.name")}</FieldLabel>
              <Input
                value={editKeyForm.name}
                onChange={(e) => setEditKeyForm((prev) => ({ ...prev, name: e.target.value }))}
              />
            </div>
            <div className="space-y-2">
              <FieldLabel>{localizedMessage(isZh, "v2.api-keys.validity")}</FieldLabel>
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
                      {localizedMessage(isZh, option.label)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="secondary" onClick={() => setEditKeyDialogFor(null)}>
              {localizedMessage(isZh, "v2.providers.cancel")}
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
              {updateKeyMut.isPending ? (localizedMessage(isZh, "v2.providers.saving")) : (localizedMessage(isZh, "v2.api-keys.save"))}
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
        confirmText={localizedMessage(isZh, "v2.providers.ok")}
        onConfirm={() => setErrorDialog(null)}
      />
    </PageLayout>
  );
}
