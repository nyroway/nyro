/* eslint-disable react-hooks/set-state-in-effect */

import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useLocation, useNavigate } from "react-router-dom";
import clsx from "clsx";
import {
  Check,
  ChevronLeft,
  ChevronRight,
  Copy,
  Filter,
  Pencil,
  Plus,
  RotateCw,
  Search,
  Trash2,
  ToggleLeft,
  ToggleRight,
  X,
} from "lucide-react";

import { formatKeyPreview } from "@/lib/format";
import type {
  Consumer,
  ConsumerKey,
  CreateConsumer,
  CreateConsumerKey,
  Route,
  UpdateConsumer,
  UpdateConsumerKey,
} from "@/lib/types";
import { PROTOCOL_TABLE, protocolDisplayName } from "@/lib/protocol";
import { useLocale } from "@/lib/i18n";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { NyroHelpHint, NyroMultiCheck, NyroSearchSelect, NyroTextField } from "@/components/ui/nyro-fields";
import { DataTable, type DataTableColumn } from "@/components/v2/data-table";
import { EmptyState } from "@/components/v2/empty-state";
import { FilterBar } from "@/components/v2/filter-bar";
import { FormSection } from "@/components/v2/form-section";
import { MetricLedger } from "@/components/v2/metric-ledger";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { ResourceEditorDrawer } from "@/components/v2/resource-editor-dialog";
import { Status } from "@/components/v2/status";
import { filterConsumers, type ConsumerFilters } from "@/features/consumers/consumer-view-model";
import {
  buildAccessListPayload,
  buildLimitsPayload,
  buildQuotasPayload,
  digitsOnly,
  emptyLimitsForm,
  emptyQuotaForm,
  expirePresetOptions,
  formatExpiresText,
  formatQuotaRule,
  formatValidityLabel,
  ipAllowlistToForm,
  isApiKeyExpired,
  isValidIPOrCIDR,
  limitsToForm,
  quotasToForm,
  quotaWindowOptions,
  resolveExpiresAt,
  resolveExpiresAtForUpdate,
  type ExpirePreset,
  type LimitsFormState,
  type QuotaFormState,
  type QuotaRuleForm,
  type RevealedKey,
} from "@/features/consumers/consumer-form";
import { RevealKeyDialog } from "@/features/consumers/reveal-key-dialog";
import { consumersApi } from "@/lib/api/consumers";
import { routesApi } from "@/lib/api/routes";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import { localizedMessage, type MessageKey } from "@/lib/messages";

const PAGE_SIZE = 7;
// Sentinel empty arrays: destructuring `data` with `= []` mints a fresh array every
// render while the query is in flight, destabilizing anything downstream that depends
// on it (see providers.tsx's React #185 note). Module constants keep it stable.
const NO_CONSUMERS: Consumer[] = [];
const NO_ROUTES: Route[] = [];

const expirePresetValues = expirePresetOptions.map((option) => option.value);

function expirePresetLabel(preset: ExpirePreset): MessageKey {
  return expirePresetOptions.find((option) => option.value === preset)?.label ?? "consumers.expiry.never";
}

const protocolOptions = PROTOCOL_TABLE.map((p) => p.id);

type WindowOption = { id: string; label: string };

// CopyFullKeyButton copies a recoverable raw key (present only when the admin
// runs with --plaintext-keys) to the clipboard, briefly swapping its icon to a
// check as feedback. Module-scope so it can own per-row state without breaking
// the rules-of-hooks in the renderKeyRow map callback. §9.4 ⑤: keep the logic, reskin only.
function CopyFullKeyButton({ token, isZh }: { token: string; isZh: boolean }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="icon-action"
      data-tip={copied ? (localizedMessage(isZh, "v2.api-keys.copied")) : localizedMessage(isZh, "v2.api-keys.copyFullKey")}
      aria-label={localizedMessage(isZh, "v2.api-keys.copyFullKey")}
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
      {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
    </button>
  );
}

/* §9.4 ②: quota rule row editing switches to the baseline table vocabulary (.table + cell-actions/row-actions).
   Each row = a limit input + a window combobox (custom windows can be typed in) + delete/clear; rows with an
   empty limit are dropped by buildQuotasPayload at submit time. */
function QuotaRuleTable({
  title,
  rows,
  onRowsChange,
  isZh,
  windowOptions,
}: {
  title: string;
  rows: QuotaRuleForm[];
  onRowsChange: (rows: QuotaRuleForm[]) => void;
  isZh: boolean;
  windowOptions: WindowOption[];
}) {
  return (
    <div className="field span-2">
      <span className="field-label">{title}</span>
      <table className="table quota-table" aria-label={title}>
        <thead>
          <tr>
            <th>{localizedMessage(isZh, "v2.api-keys.limit")}</th>
            <th>{localizedMessage(isZh, "v2.api-keys.window")}</th>
            <th className="cell-actions">
              <span className="sr-only">{localizedMessage(isZh, "v2.providers.actions")}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td colSpan={3} className="quota-table-empty">
                {localizedMessage(isZh, "v2.api-keys.notSetUnlimited")}
              </td>
            </tr>
          ) : rows.map((row, idx) => {
            const windowValue = windowOptions.find((option) => option.id === row.window)
              ?? (row.window ? { id: row.window, label: row.window } : null);
            return (
              <tr key={idx}>
                <td>
                  <input
                    className="field-control"
                    type="text"
                    inputMode="numeric"
                    pattern="[0-9]*"
                    value={row.limit}
                    placeholder={localizedMessage(isZh, "v2.api-keys.limit")}
                    aria-label={localizedMessage(isZh, "v2.api-keys.limit")}
                    onChange={(event) => {
                      const next = rows.slice();
                      next[idx] = { ...row, limit: digitsOnly(event.target.value) };
                      onRowsChange(next);
                    }}
                  />
                </td>
                <td>
                  <NyroSearchSelect<WindowOption>
                    options={windowOptions}
                    value={windowValue}
                    onChange={(option) => {
                      const next = rows.slice();
                      next[idx] = { ...row, window: option?.id ?? "" };
                      onRowsChange(next);
                    }}
                    getOptionLabel={(option) => option.label}
                    isOptionEqualToValue={(a, b) => a.id === b.id}
                    createOptionFromInput={(input) => ({ id: input, label: input })}
                    searchPlaceholder={localizedMessage(isZh, "common.search")}
                    placeholder={localizedMessage(isZh, "v2.api-keys.window")}
                    noOptionsText={localizedMessage(isZh, "v2.api-keys.window")}
                    fullWidth={false}
                    ariaLabel={localizedMessage(isZh, "v2.api-keys.window")}
                  />
                </td>
                <td className="cell-actions">
                  <div className="row-actions">
                    {rows.length > 1 ? (
                      <button
                        type="button"
                        className="icon-action danger"
                        data-tip={localizedMessage(isZh, "v2.api-keys.removeRule")}
                        aria-label={localizedMessage(isZh, "v2.api-keys.removeRule")}
                        onClick={() => onRowsChange(rows.filter((_, i) => i !== idx))}
                      >
                        <Trash2 aria-hidden="true" />
                      </button>
                    ) : (
                      <button
                        type="button"
                        className="icon-action"
                        data-tip={localizedMessage(isZh, "v2.api-keys.clear")}
                        aria-label={localizedMessage(isZh, "v2.api-keys.clear")}
                        onClick={() => onRowsChange([{ limit: "", window: "" }])}
                      >
                        <X aria-hidden="true" />
                      </button>
                    )}
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <button
        type="button"
        className="button button-sm quota-add"
        onClick={() => onRowsChange([...rows, { limit: "", window: "" }])}
      >
        <Plus aria-hidden="true" />{localizedMessage(isZh, "v2.api-keys.addRule")}
      </button>
    </div>
  );
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
  windowOptions: WindowOption[];
}) {
  function updateRows(kind: "requests" | "tokens", rows: QuotaRuleForm[]) {
    onChange({ ...value, [kind]: rows });
  }

  return (
    <>
      <QuotaRuleTable
        title={localizedMessage(isZh, "v2.api-keys.rateLimit")}
        rows={value.requests}
        onRowsChange={(rows) => updateRows("requests", rows)}
        isZh={isZh}
        windowOptions={windowOptions}
      />
      <QuotaRuleTable
        title={localizedMessage(isZh, "v2.api-keys.tokenQuota")}
        rows={value.tokens}
        onRowsChange={(rows) => updateRows("tokens", rows)}
        isZh={isZh}
        windowOptions={windowOptions}
      />
      <NyroTextField
        label={localizedMessage(isZh, "v2.api-keys.concurrencyLimit")}
        help={localizedMessage(isZh, "v2.api-keys.capsTheNumberOfRequestsProcessedAtThe")}
        numbersOnly
        value={value.concurrency}
        placeholder={localizedMessage(isZh, "v2.api-keys.eG10")}
        onChange={(event) => onChange({ ...value, concurrency: event.target.value })}
      />
    </>
  );
}

/** access.ip_allowlist is edited as one input row per entry, each holding a
 *  single IP or CIDR block; invalid rows flag an error without blocking typing. */
function IPAllowlistEditor({
  value,
  onChange,
  isZh,
  help,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  isZh: boolean;
  help: string;
}) {
  const hasInvalid = value.some((ip) => ip.trim() !== "" && !isValidIPOrCIDR(ip));
  return (
    <div className="field span-2">
      <span className="field-label">
        {localizedMessage(isZh, "v2.api-keys.ipAllowlist")}
        <NyroHelpHint text={help} />
      </span>
      <div className="ip-rows">
        {value.map((ip, idx) => {
          const invalid = ip.trim() !== "" && !isValidIPOrCIDR(ip);
          return (
            <div className="ip-row" key={idx}>
              <input
                className={clsx("field-control", invalid && "is-error")}
                value={ip}
                placeholder={localizedMessage(isZh, "v2.api-keys.eG100008Or")}
                aria-label={localizedMessage(isZh, "v2.api-keys.ipAllowlist")}
                onChange={(event) => {
                  const next = value.slice();
                  next[idx] = event.target.value;
                  onChange(next);
                }}
              />
              {value.length > 1 ? (
                <button
                  type="button"
                  className="icon-action danger"
                  data-tip={localizedMessage(isZh, "v2.api-keys.remove")}
                  aria-label={localizedMessage(isZh, "v2.api-keys.remove")}
                  onClick={() => onChange(value.filter((_, i) => i !== idx))}
                >
                  <Trash2 aria-hidden="true" />
                </button>
              ) : (
                <button
                  type="button"
                  className="icon-action"
                  data-tip={localizedMessage(isZh, "v2.api-keys.clear")}
                  aria-label={localizedMessage(isZh, "v2.api-keys.clear")}
                  onClick={() => onChange([""])}
                >
                  <X aria-hidden="true" />
                </button>
              )}
            </div>
          );
        })}
        <button type="button" className="button button-sm quota-add" onClick={() => onChange([...value, ""])}>
          <Plus aria-hidden="true" />{localizedMessage(isZh, "v2.api-keys.addRule")}
        </button>
      </div>
      {hasInvalid && (
        <span className="field-hint error">{localizedMessage(isZh, "v2.api-keys.invalidIpOrCidr")}</span>
      )}
    </div>
  );
}

type ConsumerFormValues = {
  name: string;
  routes: string[];
  protocols: string[];
  ipAllowlist: string[];
  quotas: QuotaFormState;
  limits: LimitsFormState;
};

/* §9.4: the create wizard's four form-sections (basic info/access permissions/access quotas/resource limits),
   shared by create and edit; the second control in the basic-info section is injected via basicsExtra
   (create = key validity period, edit = enabled status). */
function ConsumerEditor({
  form,
  onChange,
  isZh,
  routeOptions,
  windowOptions,
  basicsExtra,
}: {
  form: ConsumerFormValues;
  onChange: (patch: Partial<ConsumerFormValues>) => void;
  isZh: boolean;
  routeOptions: string[];
  windowOptions: WindowOption[];
  basicsExtra: ReactNode;
}) {
  return (
    <div>
      <FormSection title={localizedMessage(isZh, "v2.api-keys.1BasicInformation")}>
        <NyroTextField
          label={localizedMessage(isZh, "v2.providers.name")}
          required
          value={form.name}
          placeholder={localizedMessage(isZh, "v2.api-keys.eGProduction")}
          onChange={(event) => onChange({ name: event.target.value })}
        />
        {basicsExtra}
      </FormSection>

      <FormSection title={localizedMessage(isZh, "v2.api-keys.2AccessPermission")}>
        <NyroMultiCheck<string>
          label={localizedMessage(isZh, "v2.api-keys.bindModels")}
          help={localizedMessage(isZh, "v2.api-keys.whenLeftUnboundAllProtectedModelsAreAccessible")}
          options={routeOptions}
          selected={form.routes}
          onChange={(next) => onChange({ routes: next })}
          getOptionValue={(model) => model}
          getOptionLabel={(model) => model}
          placeholder={localizedMessage(isZh, "v2.api-keys.selectProtectedModelsThisKeyCanAccess")}
        />
        <NyroMultiCheck<string>
          label={localizedMessage(isZh, "v2.api-keys.allowedProtocols")}
          help={localizedMessage(isZh, "v2.api-keys.leavingThisEmptyAppliesNoProtocolRestriction")}
          options={protocolOptions}
          selected={form.protocols}
          onChange={(next) => onChange({ protocols: next })}
          getOptionValue={(protocol) => protocol}
          getOptionLabel={(protocol) => protocolDisplayName(protocol) ?? protocol}
          placeholder={localizedMessage(isZh, "v2.api-keys.selectAllowedProtocols")}
        />
        <IPAllowlistEditor
          value={form.ipAllowlist}
          onChange={(ipAllowlist) => onChange({ ipAllowlist })}
          isZh={isZh}
          help={localizedMessage(isZh, "v2.api-keys.leavingThisEmptyAppliesNoRestrictionOnSource")}
        />
      </FormSection>

      <FormSection title={localizedMessage(isZh, "v2.api-keys.3AccessQuota")}>
        <QuotaEditor
          value={form.quotas}
          onChange={(quotas) => onChange({ quotas })}
          isZh={isZh}
          windowOptions={windowOptions}
        />
      </FormSection>

      <FormSection
        title={localizedMessage(isZh, "v2.api-keys.4ResourceLimits")}
        description={localizedMessage(isZh, "v2.api-keys.allOptionalLeaveEmptyForNoLimit")}
      >
        <div className="span-2 form-grid cols-3">
          <NyroTextField
            label={localizedMessage(isZh, "v2.api-keys.maxInputTokens")}
            numbersOnly
            fullWidth={false}
            value={form.limits.maxInputTokens}
            placeholder={localizedMessage(isZh, "v2.api-keys.eG4000")}
            onChange={(event) => onChange({ limits: { ...form.limits, maxInputTokens: event.target.value } })}
          />
          <NyroTextField
            label={localizedMessage(isZh, "v2.api-keys.maxOutputTokens")}
            numbersOnly
            fullWidth={false}
            value={form.limits.maxOutputTokens}
            placeholder={localizedMessage(isZh, "v2.api-keys.eG2000")}
            onChange={(event) => onChange({ limits: { ...form.limits, maxOutputTokens: event.target.value } })}
          />
          <NyroTextField
            label={localizedMessage(isZh, "v2.api-keys.maxRequestBodyBytes")}
            numbersOnly
            fullWidth={false}
            value={form.limits.maxRequestBodyBytes}
            placeholder={localizedMessage(isZh, "v2.api-keys.eG1048576")}
            onChange={(event) => onChange({ limits: { ...form.limits, maxRequestBodyBytes: event.target.value } })}
          />
        </div>
      </FormSection>
    </div>
  );
}

type CreateForm = ConsumerFormValues & { keyExpiresPreset: ExpirePreset };

const emptyCreate: CreateForm = {
  name: "",
  routes: [],
  protocols: [],
  ipAllowlist: [""],
  quotas: emptyQuotaForm,
  limits: emptyLimitsForm,
  keyExpiresPreset: "never",
};

type EditForm = ConsumerFormValues & { id: string; enabled: boolean };

type StatusOption = { id: string; label: string };

export default function ApiKeysPage() {
  const { locale, t } = useLocale();
  const isZh = locale === "zh-CN";
  const qc = useQueryClient();
  const location = useLocation();
  const navigate = useNavigate();

  const windowOptions = useMemo(() => quotaWindowOptions(isZh), [isZh]);

  const [showForm, setShowForm] = useState(false);
  const [createForm, setCreateForm] = useState<CreateForm>(emptyCreate);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editForm, setEditForm] = useState<EditForm | null>(null);
  const [page, setPage] = useState(0);
  const [filters, setFilters] = useState<ConsumerFilters>({ query: "", status: "all" });

  // §9.4 note (security): the raw token from the create response lives only in this one piece of state; closing
  // the dialog clears it, after which it can never be fetched again (the list only has key_preview).
  const [revealedKey, setRevealedKey] = useState<RevealedKey | null>(null);

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

  const { data: consumers = NO_CONSUMERS, isLoading } = useQuery<Consumer[]>({
    queryKey: ["consumers"],
    queryFn: () => consumersApi.list(),
  });
  const { data: routes = NO_ROUTES } = useQuery<Route[]>({
    queryKey: ["routes"],
    queryFn: () => routesApi.list(),
  });

  function invalidateConsumers() {
    return qc.invalidateQueries({ queryKey: ["consumers"] });
  }

  const createMut = useMutation({
    mutationFn: (input: CreateConsumer) => consumersApi.create(input),
    onSuccess: (created) => {
      invalidateConsumers();
      setShowForm(false);
      setCreateForm(emptyCreate);
      const firstKey = created.keys?.[0];
      if (firstKey?.token) {
        setRevealedKey({ name: firstKey.name, token: firstKey.token });
      }
    },
    onError: (error: unknown) => {
      showErrorDialog("consumers.error.create", error);
    },
  });

  const updateMut = useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateConsumer }) => consumersApi.update(id, input),
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
    mutationFn: (id: string) => consumersApi.remove(id),
    onSuccess: () => invalidateConsumers(),
    onError: (error: unknown) => {
      showErrorDialog("consumers.error.delete", error);
    },
  });

  const toggleConsumerEnabledMut = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => consumersApi.update(id, { enabled }),
    onSuccess: () => invalidateConsumers(),
    onError: (error: unknown) => {
      showErrorDialog("consumers.error.operation", error);
    },
  });

  const addKeyMut = useMutation({
    mutationFn: ({ consumerId, input }: { consumerId: string; input: CreateConsumerKey }) =>
      consumersApi.addKey(consumerId, input),
    onSuccess: (created) => {
      invalidateConsumers();
      setAddKeyDialogFor(null);
      if (created.token) {
        setRevealedKey({ name: created.name, token: created.token });
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
    }) => consumersApi.updateKey(consumerId, keyId, input),
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
      const created = await consumersApi.addKey(consumerId, {
        name: tempName,
        expires_at: key.expires_at,
      });
      await consumersApi.removeKey(consumerId, key.id);
      await consumersApi.updateKey(consumerId, created.id, { name: key.name });
      return { ...created, name: key.name };
    },
    onSuccess: (created) => {
      invalidateConsumers();
      if (created.token) {
        setRevealedKey({ name: created.name, token: created.token });
      }
    },
    onError: (error: unknown) => {
      showErrorDialog("consumers.error.regenerateKey", error);
    },
  });

  const deleteKeyMut = useMutation({
    mutationFn: ({ consumerId, keyId }: { consumerId: string; keyId: string }) =>
      consumersApi.removeKey(consumerId, keyId),
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
      routes.map((route) => route.model),
    [routes],
  );

  // Derive the editing consumer from live query data (not a stale snapshot), so
  // the keys section reflects add/delete/regenerate without reopening the dialog.
  const editingConsumer = useMemo(
    () => consumers.find((item) => item.id === editingId) ?? null,
    [consumers, editingId],
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
      setEditForm(null);
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

  function renderKeyRow(consumer: Consumer, key: ConsumerKey) {
    const keyExpired = isApiKeyExpired(key.expires_at);
    // key.token is only present on read when admin runs with --plaintext-keys
    // (recoverable storage); otherwise the full key is shown once at creation.
    const recoverableToken = key.token;
    return (
      <div key={key.id} className="key-row">
        <div className="key-row-copy">
          <strong>{key.name}</strong>
          <code
            title={
              recoverableToken
                ? (localizedMessage(isZh, "v2.api-keys.plaintextStorageIsOnTheFullKeyCan"))
                : localizedMessage(isZh, "v2.api-keys.fullKeyIsShownOnceOnCreateAdd")
            }
          >
            {formatKeyPreview(key.key_preview)}
          </code>
          {recoverableToken && <CopyFullKeyButton token={recoverableToken} isZh={isZh} />}
        </div>
        <div className="key-row-meta">
          {!key.enabled && (
            <Status tone="danger">{localizedMessage(isZh, "v2.providers.disabled2")}</Status>
          )}
          <Status tone={keyExpired ? "danger" : "success"}>
            {formatValidityLabel(keyExpired, isZh)}
          </Status>
          <span>{formatExpiresText(key.expires_at, isZh)}</span>
        </div>
        <div className="row-actions">
          <button
            type="button"
            className="icon-action"
            data-tip={key.enabled ? (localizedMessage(isZh, "v2.providers.disable")) : (localizedMessage(isZh, "v2.providers.enable"))}
            aria-label={key.enabled ? (localizedMessage(isZh, "v2.providers.disable")) : (localizedMessage(isZh, "v2.providers.enable"))}
            onClick={() => updateKeyMut.mutate({ consumerId: consumer.id, keyId: key.id, input: { enabled: !key.enabled } })}
          >
            {key.enabled ? <ToggleRight aria-hidden="true" /> : <ToggleLeft aria-hidden="true" />}
          </button>
          <button
            type="button"
            className="icon-action"
            data-tip={localizedMessage(isZh, "v2.api-keys.editNameValidity")}
            aria-label={localizedMessage(isZh, "v2.api-keys.editNameValidity")}
            onClick={() => openEditKeyDialog(consumer, key)}
          >
            <Pencil aria-hidden="true" />
          </button>
          <button
            type="button"
            className="icon-action"
            data-tip={localizedMessage(isZh, "v2.api-keys.regenerateKeyOldKeyIsInvalidatedImmediately")}
            aria-label={localizedMessage(isZh, "v2.api-keys.regenerateKeyOldKeyIsInvalidatedImmediately")}
            onClick={() => setKeyToRegenerate({ consumer, key })}
          >
            <RotateCw aria-hidden="true" />
          </button>
          <button
            type="button"
            className="icon-action danger"
            data-tip={localizedMessage(isZh, "v2.api-keys.deleteKey")}
            aria-label={localizedMessage(isZh, "v2.api-keys.deleteKey")}
            onClick={() => setKeyToDelete({ consumer, key })}
          >
            <Trash2 aria-hidden="true" />
          </button>
        </div>
      </div>
    );
  }

  function renderKeysSection(consumer: Consumer) {
    const keys = consumer.keys ?? [];
    if (keys.length === 0) {
      return (
        <div className="key-row key-row-empty">
          <p>{localizedMessage(isZh, "v2.api-keys.noKeyYetCannotAuthenticate")}</p>
          <button type="button" className="button button-sm" onClick={() => openAddKeyDialog(consumer)}>
            <Plus aria-hidden="true" />{localizedMessage(isZh, "v2.api-keys.addKey")}
          </button>
        </div>
      );
    }
    return keys.map((key) => renderKeyRow(consumer, key));
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

  const statusOptions = useMemo<StatusOption[]>(() => [
    { id: "all", label: localizedMessage(isZh, "v2.providers.allStatuses") },
    { id: "enabled", label: localizedMessage(isZh, "v2.api-keys.enabled") },
    { id: "disabled", label: localizedMessage(isZh, "v2.api-keys.disabled") },
  ], [isZh]);
  const statusValue = statusOptions.find((option) => option.id === filters.status) ?? statusOptions[0];

  const consumerColumns: DataTableColumn<Consumer>[] = [
    {
      key: "consumer",
      header: localizedMessage(isZh, "v2.api-keys.consumer"),
      render: (consumer) => (
        <div className="cell-stack">
          <strong>{consumer.name}</strong>
          <code>{consumer.id}</code>
        </div>
      ),
    },
    {
      key: "keys",
      header: localizedMessage(isZh, "v2.api-keys.keys"),
      render: (consumer) => {
        const keys = consumer.keys ?? [];
        const invalid = keys.filter((key) => !key.enabled || isApiKeyExpired(key.expires_at)).length;
        return <div className="cell-stack"><strong>{keys.length}</strong><span>{invalid ? localizedMessage(isZh, "common.unavailableCount", { count: invalid }) : localizedMessage(isZh, "v2.api-keys.allValid")}</span></div>;
      },
    },
    {
      key: "routes",
      header: localizedMessage(isZh, "v2.api-keys.modelAccess"),
      render: (consumer) => consumer.routes?.length
        ? <div className="cell-tags">{consumer.routes.slice(0, 2).map((route) => <span className="tag" key={route}>{route}</span>)}{consumer.routes.length > 2 && <span className="cell-tags-more">+{consumer.routes.length - 2}</span>}</div>
        : <span className="cell-note">{localizedMessage(isZh, "v2.api-keys.allModels")}</span>,
    },
    {
      key: "restrictions",
      header: localizedMessage(isZh, "v2.api-keys.restrictions"),
      render: (consumer) => (
        <div className="cell-stack">
          <strong>{consumer.protocols?.length ? consumer.protocols.join(" / ") : (localizedMessage(isZh, "v2.providers.allProtocols"))}</strong>
          <span>{consumer.ip_allowlist?.length ? localizedMessage(isZh, "consumers.ipRules", { count: consumer.ip_allowlist.length }) : (localizedMessage(isZh, "v2.api-keys.anySourceIp"))}</span>
        </div>
      ),
    },
    {
      key: "quotas",
      header: localizedMessage(isZh, "v2.api-keys.quotas"),
      render: (consumer) => consumer.quotas?.length
        ? <div className="cell-stack"><strong>{formatQuotaRule(consumer.quotas[0])}</strong><span>{consumer.quotas.length > 1 ? localizedMessage(isZh, "common.additionalRules", { count: consumer.quotas.length - 1 }) : (localizedMessage(isZh, "v2.api-keys.1Rule"))}</span></div>
        : <span className="cell-note">{localizedMessage(isZh, "v2.api-keys.unlimited")}</span>,
    },
    {
      key: "status",
      header: localizedMessage(isZh, "v2.providers.status"),
      render: (consumer) => <Status tone={consumer.enabled ? "success" : "neutral"}>{consumer.enabled ? (localizedMessage(isZh, "v2.api-keys.enabled")) : (localizedMessage(isZh, "v2.api-keys.disabled"))}</Status>,
    },
    {
      key: "actions",
      header: <span className="sr-only">{localizedMessage(isZh, "v2.providers.actions")}</span>,
      className: "cell-actions",
      render: (consumer) => (
        <div className="row-actions">
          <button type="button" className="icon-action" data-tip={consumer.enabled ? (localizedMessage(isZh, "v2.api-keys.disable")) : (localizedMessage(isZh, "v2.providers.enable"))} aria-label={consumer.enabled ? (localizedMessage(isZh, "v2.api-keys.disable")) : (localizedMessage(isZh, "v2.providers.enable"))} onClick={(event) => { event.stopPropagation(); toggleConsumerEnabledMut.mutate({ id: consumer.id, enabled: !consumer.enabled }); }}>{consumer.enabled ? <ToggleRight aria-hidden="true" /> : <ToggleLeft aria-hidden="true" />}</button>
          <button type="button" className="icon-action" data-tip={localizedMessage(isZh, "v2.api-keys.addKey")} aria-label={localizedMessage(isZh, "v2.api-keys.addKey")} onClick={(event) => { event.stopPropagation(); openAddKeyDialog(consumer); }}><Plus aria-hidden="true" /></button>
          <button type="button" className="icon-action" data-tip={localizedMessage(isZh, "v2.providers.edit")} aria-label={localizedMessage(isZh, "v2.providers.edit")} onClick={(event) => { event.stopPropagation(); startEdit(consumer); }}><Pencil aria-hidden="true" /></button>
          <button type="button" className="icon-action danger" data-tip={localizedMessage(isZh, "v2.providers.delete")} aria-label={localizedMessage(isZh, "v2.providers.delete")} onClick={(event) => { event.stopPropagation(); setConsumerToDelete(consumer); }}><Trash2 aria-hidden="true" /></button>
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
        />
      )}
    >

      <MetricLedger items={[
        { key: "consumers", label: localizedMessage(isZh, "v2.api-keys.consumers"), value: consumers.length, detail: localizedMessage(isZh, "common.enabledCount", { count: consumers.filter((item) => item.enabled).length }) },
        { key: "active-keys", label: localizedMessage(isZh, "v2.api-keys.activeKeys"), value: activeKeyCount, detail: localizedMessage(isZh, "consumers.keysTotal", { count: keyCount }), tone: "success" },
        { key: "expiring", label: localizedMessage(isZh, "v2.api-keys.expiringSoon"), value: expiringKeyCount, detail: localizedMessage(isZh, "v2.api-keys.within10Days"), tone: expiringKeyCount ? "warning" : "default" },
        { key: "restricted", label: localizedMessage(isZh, "v2.api-keys.withQuotas"), value: consumers.filter((item) => item.quotas?.length).length, detail: localizedMessage(isZh, "v2.api-keys.consumerLevelLimits") },
      ]} />

      <FilterBar>
        <label className="toolbar-search">
          <Search aria-hidden="true" />
          <input
            aria-label={localizedMessage(isZh, "v2.api-keys.searchConsumers")}
            placeholder={localizedMessage(isZh, "v2.api-keys.searchConsumerModelOrProtocol")}
            value={filters.query}
            onChange={(event) => setFilters((current) => ({ ...current, query: event.target.value }))}
          />
        </label>
        <NyroSearchSelect<StatusOption>
          options={statusOptions}
          value={statusValue}
          onChange={(next) => { setFilters((current) => ({ ...current, status: (next?.id ?? "all") as ConsumerFilters["status"] })); }}
          getOptionLabel={(option) => option.label}
          isOptionEqualToValue={(a, b) => a.id === b.id}
          searchable={false}
          fullWidth={false}
          controlClassName="toolbar-filter"
          leadingIcon={<Filter size={14} aria-hidden="true" />}
          ariaLabel={localizedMessage(isZh, "v2.providers.filterByStatus")}
        />
        <button
          type="button"
          className="button button-primary button-sm toolbar-add"
          onClick={() => { setEditingId(null); setEditForm(null); setCreateForm(emptyCreate); setShowForm(true); }}
        >
          {localizedMessage(isZh, "v2.api-keys.addConsumer")}
        </button>
      </FilterBar>

      <DataTable
        carded
        columns={consumerColumns}
        rows={pagedConsumers}
        rowKey={(consumer) => consumer.id}
        loading={isLoading}
        onRowClick={startEdit}
        empty={<EmptyState
          title={consumers.length ? (localizedMessage(isZh, "v2.api-keys.noMatchingConsumers")) : (localizedMessage(isZh, "v2.api-keys.noConsumersYet"))}
          description={consumers.length ? (localizedMessage(isZh, "v2.api-keys.adjustTheSearchOrStatusFilter")) : (localizedMessage(isZh, "v2.api-keys.createAConsumerToGenerateItsFirstApi"))}
          action={!consumers.length ? (
            <button type="button" className="button button-primary" onClick={() => setShowForm(true)}>
              {localizedMessage(isZh, "v2.api-keys.addConsumer")}
            </button>
          ) : undefined}
        />}
        footer={<>
          <span className="table-summary">{localizedMessage(isZh, "common.showing", { visible: filteredConsumers.length, total: consumers.length })}</span>
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
        open={showForm}
        title={localizedMessage(isZh, "v2.api-keys.addConsumer2")}
        description={localizedMessage(isZh, "v2.api-keys.createAnIdentityWithModelAccessRestrictionsAnd")}
        onClose={() => { setShowForm(false); setCreateForm(emptyCreate); }}
        footer={<>
          <button
            type="button"
            className="button button-secondary"
            onClick={() => {
              setShowForm(false);
              setCreateForm(emptyCreate);
            }}
          >
            {localizedMessage(isZh, "v2.providers.cancel")}
          </button>
          <button
            type="button"
            className="button button-primary"
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
          </button>
        </>}
      >
        <ConsumerEditor
          form={createForm}
          onChange={(patch) => setCreateForm((prev) => ({ ...prev, ...patch }))}
          isZh={isZh}
          routeOptions={routeOptions}
          windowOptions={windowOptions}
          basicsExtra={(
            <NyroSearchSelect<ExpirePreset>
              label={localizedMessage(isZh, "v2.api-keys.keyValidity")}
              options={expirePresetValues}
              value={createForm.keyExpiresPreset}
              onChange={(preset) => setCreateForm((prev) => ({ ...prev, keyExpiresPreset: preset ?? "never" }))}
              getOptionLabel={(preset) => localizedMessage(isZh, expirePresetLabel(preset))}
              isOptionEqualToValue={(a, b) => a === b}
              searchable={false}
              hint={localizedMessage(isZh, "v2.api-keys.theKeyValueIsAutoGeneratedAndShown")}
            />
          )}
        />
      </ResourceEditorDrawer>

      <ResourceEditorDrawer
        open={Boolean(editingConsumer && editForm)}
        title={localizedMessage(isZh, "consumers.edit")}
        description={editingConsumer?.name}
        onClose={() => {
          setEditingId(null);
          setEditForm(null);
        }}
        footer={editForm ? (<>
          <button
            type="button"
            className="button button-secondary"
            onClick={() => {
              setEditingId(null);
              setEditForm(null);
            }}
          >
            {localizedMessage(isZh, "v2.providers.cancel")}
          </button>
          <button
            type="button"
            className="button button-primary"
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
            disabled={
              updateMut.isPending ||
              !editForm.name.trim() ||
              editForm.ipAllowlist.some((ip) => !isValidIPOrCIDR(ip))
            }
          >
            {updateMut.isPending ? (localizedMessage(isZh, "v2.providers.saving")) : (localizedMessage(isZh, "v2.api-keys.save"))}
          </button>
        </>) : undefined}
      >
        {editForm && (
          <>
            <ConsumerEditor
              form={editForm}
              onChange={(patch) => setEditForm((prev) => (prev ? { ...prev, ...patch } : prev))}
              isZh={isZh}
              routeOptions={routeOptions}
              windowOptions={windowOptions}
              basicsExtra={(
                <div className="field full">
                  <div className="switch-row">
                    <div className="switch-copy">
                      <strong>{localizedMessage(isZh, "v2.providers.status")}</strong>
                      <span>{editForm.enabled ? (localizedMessage(isZh, "v2.providers.enabled")) : (localizedMessage(isZh, "v2.providers.disabled2"))}</span>
                    </div>
                    <button
                      type="button"
                      className={clsx("switch-control", editForm.enabled && "on")}
                      aria-pressed={editForm.enabled}
                      aria-label={localizedMessage(isZh, "v2.providers.status")}
                      onClick={() => setEditForm((prev) => (prev ? { ...prev, enabled: !prev.enabled } : prev))}
                    />
                  </div>
                </div>
              )}
            />
            {editingConsumer && (
              <FormSection title={localizedMessage(isZh, "v2.api-keys.keys")}>
                <div className="span-2 key-list">
                  {renderKeysSection(editingConsumer)}
                </div>
              </FormSection>
            )}
          </>
        )}
      </ResourceEditorDrawer>

      <RevealKeyDialog revealed={revealedKey} onClose={() => setRevealedKey(null)} />

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

      <ResourceEditorDrawer
        open={Boolean(addKeyDialogFor)}
        title={localizedMessage(isZh, "v2.api-keys.addKey2")}
        description={localizedMessage(isZh, "v2.api-keys.theKeyValueIsAutoGeneratedAndShown")}
        onClose={() => setAddKeyDialogFor(null)}
        footer={<>
          <button type="button" className="button button-secondary" onClick={() => setAddKeyDialogFor(null)}>
            {localizedMessage(isZh, "v2.providers.cancel")}
          </button>
          <button
            type="button"
            className="button button-primary"
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
          </button>
        </>}
      >
        <div className="form-grid">
          <NyroTextField
            label={localizedMessage(isZh, "v2.providers.name")}
            required
            value={addKeyForm.name}
            placeholder={localizedMessage(isZh, "v2.api-keys.eGDefault")}
            onChange={(event) => setAddKeyForm((prev) => ({ ...prev, name: event.target.value }))}
          />
          <NyroSearchSelect<ExpirePreset>
            label={localizedMessage(isZh, "v2.api-keys.validity")}
            options={expirePresetValues}
            value={addKeyForm.expiresPreset}
            onChange={(preset) => setAddKeyForm((prev) => ({ ...prev, expiresPreset: preset ?? "never" }))}
            getOptionLabel={(preset) => localizedMessage(isZh, expirePresetLabel(preset))}
            isOptionEqualToValue={(a, b) => a === b}
            searchable={false}
          />
        </div>
      </ResourceEditorDrawer>

      <ResourceEditorDrawer
        open={Boolean(editKeyDialogFor)}
        title={localizedMessage(isZh, "v2.api-keys.editKey2")}
        description={editKeyDialogFor?.key.name}
        onClose={() => setEditKeyDialogFor(null)}
        footer={<>
          <button type="button" className="button button-secondary" onClick={() => setEditKeyDialogFor(null)}>
            {localizedMessage(isZh, "v2.providers.cancel")}
          </button>
          <button
            type="button"
            className="button button-primary"
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
          </button>
        </>}
      >
        <div className="form-grid">
          <NyroTextField
            label={localizedMessage(isZh, "v2.providers.name")}
            required
            value={editKeyForm.name}
            onChange={(event) => setEditKeyForm((prev) => ({ ...prev, name: event.target.value }))}
          />
          <NyroSearchSelect<ExpirePreset>
            label={localizedMessage(isZh, "v2.api-keys.validity")}
            options={expirePresetValues}
            value={editKeyForm.expiresPreset}
            onChange={(preset) => setEditKeyForm((prev) => (preset ? { ...prev, expiresPreset: preset, expiresTouched: true } : prev))}
            getOptionLabel={(preset) => localizedMessage(isZh, expirePresetLabel(preset))}
            isOptionEqualToValue={(a, b) => a === b}
            searchable={false}
          />
        </div>
      </ResourceEditorDrawer>

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
