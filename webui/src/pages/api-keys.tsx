import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronLeft, ChevronRight, Copy, KeyRound, Pencil, Plus, Trash2 } from "lucide-react";

import { backend } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type { ApiKey, CreateApiKey, Model as ModelType, UpdateApiKey } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { CopyValue } from "@/components/ui/copy-value";
import { DetailList } from "@/components/ui/detail-list";
import { showToast } from "@/lib/toast";
import { copyableTipProps } from "@/lib/copyable";
import { PageHeader, PageLayout } from "@/components/layout/page";
import { Drawer, Modal } from "@/components/ui/overlays";
import { NyroSelect } from "@/components/ui/nyro-select";
import { localizedMessage, type MessageKey } from "@/lib/messages";


const PAGE_SIZE = 7;

type ExpirePreset = "never" | "1d" | "7d" | "30d" | "90d" | "180d" | "1y";

const expirePresetOptions: { value: ExpirePreset; label: MessageKey }[] = [
  { value: "never", label: "apiKeys.never" },
  { value: "1d", label: "apiKeys.expire1Day" },
  { value: "7d", label: "apiKeys.expire7Days" },
  { value: "30d", label: "apiKeys.expire30Days" },
  { value: "90d", label: "apiKeys.expire90Days" },
  { value: "180d", label: "apiKeys.expire180Days" },
  { value: "1y", label: "apiKeys.expire1Year" },
];

function shortApiKeyTag(key: string) {
  const trimmed = key.trim();
  if (!trimmed) return "sk-";
  if (trimmed.length <= 14) return trimmed;
  return `${trimmed.slice(0, 7)}···${trimmed.slice(-4)}`;
}

function quotaText(value: number | null | undefined) {
  return value && value > 0 ? String(value) : "∞";
}

function formatExpiresText(value: string | null | undefined, isZh: boolean) {
  if (!value) return localizedMessage(isZh, "apiKeys.never");
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
  return expired ? (localizedMessage(isZh, "apiKeys.expired")) : (localizedMessage(isZh, "apiKeys.valid"));
}

function boundModelNames(item: ApiKey, routes: ModelType[]) {
  return (item.model_ids ?? []).map((id) => routes.find((route) => route.id === id)?.name ?? id);
}

function ModelNameTags({ names, empty, limit = 2 }: { names: string[]; empty: string; limit?: number }) {
  if (names.length === 0) return <span className="cell-note">{empty}</span>;
  const visible = limit > 0 ? names.slice(0, limit) : names;
  const extra = names.length - visible.length;
  return (
    <div className="cell-tags">
      {visible.map((name, index) => (
        <span key={`${name}-${index}`} className="tag" {...copyableTipProps(name)}>{name}</span>
      ))}
      {extra > 0 ? <span className="cell-tags-more">+{extra}</span> : null}
    </div>
  );
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

type RouteOption = { value: string; label: string };

function ModelBindingField({
  label,
  options,
  values,
  placeholder,
  emptyText,
  onChange,
}: {
  label: string;
  options: RouteOption[];
  values: string[];
  placeholder: string;
  emptyText: string;
  onChange: (next: string[]) => void;
}) {
  return (
    <div className="field">
      <span className="field-label">{label}</span>
      {options.length === 0 ? (
        <p className="field-hint">{emptyText}</p>
      ) : (
        <>
          <div className="form-stack">
            {options.map((option) => {
              const checked = values.includes(option.value);
              return (
                <label key={option.value} className={checked ? "checkbox checked" : "checkbox"}>
                  <input
                    type="checkbox"
                    className="sr-only"
                    checked={checked}
                    onChange={() => {
                      onChange(
                        checked
                          ? values.filter((id) => id !== option.value)
                          : [...values, option.value],
                      );
                    }}
                  />
                  <span className="checkbox-mark" aria-hidden="true"><Check /></span>
                  {option.label}
                </label>
              );
            })}
          </div>
          {values.length === 0 && <span className="field-hint">{placeholder}</span>}
        </>
      )}
    </div>
  );
}

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
  const [selectedKey, setSelectedKey] = useState<ApiKey | null>(null);
  const [apiKeyToDelete, setApiKeyToDelete] = useState<ApiKey | null>(null);
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
      showErrorDialog("apiKeys.failedToCreateAPIKey", error);
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
      showErrorDialog("apiKeys.failedToSaveAPIKey", error);
    },
  });
  const deleteMut = useMutation({
    mutationFn: (id: string) => backend("delete_api_key", { id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["api-keys"] }),
    onError: (error: unknown) => {
      showErrorDialog("apiKeys.failedToDeleteAPIKey", error);
    },
  });

  const toggleEnabledMut = useMutation({
    mutationFn: ({ id, is_enabled }: { id: string; is_enabled: boolean }) =>
      backend("update_api_key", { id, input: { is_enabled } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["api-keys"] }),
    onError: (error: unknown) => {
      showErrorDialog("common.operationFailed", error);
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
    showToast(localizedMessage(isZh, "common.copied"));
    setTimeout(() => setCopiedId(null), 1500);
  }

  function closeEdit() {
    setEditingId(null);
    setEditForm(null);
    setCopiedEditKey(false);
  }

  function closeCreated() {
    setShowCreatedDialog(false);
    setCopiedCreatedKey(false);
  }

  const unlimitedPlaceholder = localizedMessage(isZh, "apiKeys.emptyUnlimited");

  return (
    <PageLayout
      header={(
        <PageHeader
          title={localizedMessage(isZh, "apiKeys.apiKeys")}
          description={localizedMessage(isZh, "apiKeys.manageAuthenticationKeysQuotasAndModelBindings")}
          actions={(
            <button
              type="button"
              className="button button-primary"
              onClick={() => {
                setEditingId(null);
                setEditForm(null);
                setCopiedEditKey(false);
                setShowForm((v) => !v);
              }}
            >
              <Plus />
              {localizedMessage(isZh, "apiKeys.addKey")}
            </button>
          )}
        />
      )}
    >
      {/* List card. Create and edit use the right-side drawer; the one-time key uses a centered modal. */}
      <div className="card table-card">
        <div className="card-header">
          <h2 className="card-title">{localizedMessage(isZh, "apiKeys.keyList")}</h2>
        </div>
        <div className="table-toolbar">
          <span className="table-summary">
            {localizedMessage(isZh, "apiKeys.keys", { a: apiKeys.length })}
          </span>
        </div>
        {isLoading ? (
          <div className="card-body">
            <div className="empty"><strong>{localizedMessage(isZh, "common.loading")}</strong></div>
          </div>
        ) : apiKeys.length === 0 ? (
          <div className="card-body">
            <div className="empty">
              <KeyRound size={40} />
              <strong>{localizedMessage(isZh, "apiKeys.noAPIKeysYet")}</strong>
            </div>
          </div>
        ) : (
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>{localizedMessage(isZh, "common.name")}</th>
                  <th>{localizedMessage(isZh, "common.models")}</th>
                  <th>Key</th>
                  <th>{localizedMessage(isZh, "common.status")}</th>
                  <th>{localizedMessage(isZh, "apiKeys.quota")}</th>
                  <th className="cell-switch">{localizedMessage(isZh, "common.enabled")}</th>
                  <th className="cell-actions">{localizedMessage(isZh, "common.actions")}</th>
                </tr>
              </thead>
              <tbody>
                {pagedApiKeys.map((item) => {
                  const keyExpired = isApiKeyExpired(item.expires_at);
                  const modelNames = boundModelNames(item, routes);
                  return (
                    <tr
                      key={item.id}
                      className="clickable"
                      onClick={() => {
                        setShowForm(false);
                        setEditingId(null);
                        setEditForm(null);
                        setSelectedKey(item);
                      }}
                    >
                      <td>
                        <div className="gateway-name">
                          <span className="gateway-token"><KeyRound /></span>
                          <strong className="name-ellipsis" {...copyableTipProps(item.name)}>{item.name}</strong>
                        </div>
                      </td>
                      <td>
                        <ModelNameTags names={modelNames} empty={localizedMessage(isZh, "apiKeys.allModels")} />
                      </td>
                      <td>
                        <span className="key-mask">{shortApiKeyTag(item.key)}</span>
                      </td>
                      <td>
                        <span className={keyExpired ? "health danger" : "health"}>
                          <span className="mini-dot" />
                          {formatValidityLabel(keyExpired, isZh)}
                        </span>
                      </td>
                      <td>
                        <div className="cell-tags">
                          <span className="tag tag-neutral">RPM {quotaText(item.rpm)}</span>
                          <span className="tag tag-neutral">RPD {quotaText(item.rpd)}</span>
                          <span className="tag tag-neutral">TPM {quotaText(item.tpm)}</span>
                          <span className="tag tag-neutral">TPD {quotaText(item.tpd)}</span>
                        </div>
                      </td>
                      <td className="cell-switch">
                        <button
                          type="button"
                          role="switch"
                          className={item.is_enabled ? "switch-control sm on" : "switch-control sm"}
                          aria-checked={item.is_enabled}
                          data-tip={item.is_enabled ? (localizedMessage(isZh, "common.disable")) : (localizedMessage(isZh, "common.enable"))}
                          aria-label={item.is_enabled ? (localizedMessage(isZh, "common.disable")) : (localizedMessage(isZh, "common.enable"))}
                          onClick={(event) => {
                            event.stopPropagation();
                            toggleEnabledMut.mutate({ id: item.id, is_enabled: !item.is_enabled });
                          }}
                        />
                      </td>
                      <td className="cell-actions">
                        <div className="row-actions">
                          <button
                            type="button"
                            className="icon-action"
                            data-tip={copiedId === item.id ? (localizedMessage(isZh, "apiKeys.copied")) : (localizedMessage(isZh, "apiKeys.copyKey"))}
                            aria-label={copiedId === item.id ? (localizedMessage(isZh, "apiKeys.copied")) : (localizedMessage(isZh, "apiKeys.copyKey"))}
                            onClick={(event) => {
                              event.stopPropagation();
                              void copyKey(item);
                            }}
                          >
                            {copiedId === item.id ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
                          </button>
                          <button
                            type="button"
                            className="icon-action"
                            data-tip={localizedMessage(isZh, "common.edit")}
                            aria-label={localizedMessage(isZh, "common.edit")}
                            onClick={(event) => {
                              event.stopPropagation();
                              setSelectedKey(null);
                              startEdit(item);
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
                              setApiKeyToDelete(item);
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
        {apiKeys.length > PAGE_SIZE && (
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
        open={Boolean(selectedKey)}
        title={selectedKey?.name ?? localizedMessage(isZh, "apiKeys.keyDetails")}
        description={selectedKey?.id}
        onClose={() => setSelectedKey(null)}
        footerStart={selectedKey ? (
          <button
            type="button"
            className="button button-secondary button-danger-text"
            onClick={() => {
              const item = selectedKey;
              setSelectedKey(null);
              setApiKeyToDelete(item);
            }}
          >
            {localizedMessage(isZh, "common.delete")}
          </button>
        ) : undefined}
        footer={selectedKey ? (
          <>
            <button type="button" className="button button-secondary" onClick={() => setSelectedKey(null)}>
              {localizedMessage(isZh, "common.close")}
            </button>
            <button
              type="button"
              className="button button-primary"
              onClick={() => {
                const item = selectedKey;
                setSelectedKey(null);
                startEdit(item);
              }}
            >
              {localizedMessage(isZh, "common.edit")}
            </button>
          </>
        ) : undefined}
      >
        {selectedKey ? (
          <DetailList
            items={[
              { label: localizedMessage(isZh, "common.name"), value: <CopyValue value={selectedKey.name} isZh={isZh} /> },
              { label: localizedMessage(isZh, "apiKeys.keyValue"), value: <CopyValue value={selectedKey.key} display={shortApiKeyTag(selectedKey.key)} isZh={isZh} /> },
              { label: localizedMessage(isZh, "common.status"), value: formatValidityLabel(isApiKeyExpired(selectedKey.expires_at), isZh) },
              { label: localizedMessage(isZh, "apiKeys.expiresAt"), value: formatExpiresText(selectedKey.expires_at, isZh) },
              { label: localizedMessage(isZh, "common.models"), value: (
                <ModelNameTags
                  names={boundModelNames(selectedKey, routes)}
                  empty={localizedMessage(isZh, "apiKeys.allModels")}
                  limit={0}
                />
              ) },
              { label: "RPM", value: quotaText(selectedKey.rpm) },
              { label: "RPD", value: quotaText(selectedKey.rpd) },
              { label: "TPM", value: quotaText(selectedKey.tpm) },
              { label: "TPD", value: quotaText(selectedKey.tpd) },
              { label: localizedMessage(isZh, "common.enabled"), value: selectedKey.is_enabled ? localizedMessage(isZh, "common.enabled2") : localizedMessage(isZh, "common.disabled") },
              { label: localizedMessage(isZh, "common.createdAt"), value: selectedKey.created_at },
              { label: localizedMessage(isZh, "common.updatedAt"), value: selectedKey.updated_at },
            ]}
          />
        ) : null}
      </Drawer>

      <Drawer
        open={showForm}
        wide
        title={localizedMessage(isZh, "apiKeys.createAPIKey")}
        description={localizedMessage(isZh, "apiKeys.keyValueIsAutoGeneratedAfterCreation")}
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
              {createMut.isPending ? (localizedMessage(isZh, "common.creating")) : (localizedMessage(isZh, "common.create"))}
            </button>
          </>
        )}
      >
        <div className="form-stack">
          <div>
            <p className="form-section-title">{localizedMessage(isZh, "apiKeys.text1BasicInformation")}</p>
            <p className="form-section-desc">
              {localizedMessage(isZh, "apiKeys.keyValueIsAutoGeneratedAfterCreation")}
            </p>
            <div className="form-grid">
              <label className="field">
                <span className="field-label">{localizedMessage(isZh, "common.name")}</span>
                <input
                  className="field-control"
                  value={createForm.name}
                  onChange={(e) => setCreateForm((prev) => ({ ...prev, name: e.target.value }))}
                  placeholder={localizedMessage(isZh, "apiKeys.eGFrontendApp")}
                />
              </label>
              <div className="field">
                <span className="field-label">{localizedMessage(isZh, "apiKeys.validity")}</span>
                <NyroSelect
                  ariaLabel={localizedMessage(isZh, "apiKeys.validity")}
                  value={createForm.expiresPreset}
                  options={expirePresetOptions.map((option) => ({
                    value: option.value,
                    label: localizedMessage(isZh, option.label),
                  }))}
                  onChange={(value) => setCreateForm((prev) => ({ ...prev, expiresPreset: value as ExpirePreset }))}
                />
              </div>
            </div>
          </div>

          <div className="form-section">
            <p className="form-section-title">{localizedMessage(isZh, "apiKeys.text2AccessPermission")}</p>
            <ModelBindingField
              label={localizedMessage(isZh, "apiKeys.bindModelsNoneDenyOnProtectedModels")}
              options={routeOptions}
              values={createForm.model_ids}
              placeholder={localizedMessage(isZh, "apiKeys.selectProtectedModelsThisKeyCanAccess")}
              emptyText={localizedMessage(isZh, "apiKeys.noMatchingModels")}
              onChange={(next) => setCreateForm((prev) => ({ ...prev, model_ids: next }))}
            />
          </div>

          <div className="form-section">
            <p className="form-section-title">{localizedMessage(isZh, "apiKeys.text3AccessQuota")}</p>
            <div className="form-grid">
              <label className="field">
                <span className="field-label">TPM</span>
                <input
                  className="field-control"
                  type="text"
                  inputMode="numeric"
                  pattern="[0-9]*"
                  value={createForm.tpm}
                  onChange={(e) => setCreateForm((prev) => ({ ...prev, tpm: digitsOnly(e.target.value) }))}
                  placeholder={unlimitedPlaceholder}
                />
              </label>
              <label className="field">
                <span className="field-label">TPD</span>
                <input
                  className="field-control"
                  type="text"
                  inputMode="numeric"
                  pattern="[0-9]*"
                  value={createForm.tpd}
                  onChange={(e) => setCreateForm((prev) => ({ ...prev, tpd: digitsOnly(e.target.value) }))}
                  placeholder={unlimitedPlaceholder}
                />
              </label>
              <label className="field">
                <span className="field-label">RPM</span>
                <input
                  className="field-control"
                  type="text"
                  inputMode="numeric"
                  pattern="[0-9]*"
                  value={createForm.rpm}
                  onChange={(e) => setCreateForm((prev) => ({ ...prev, rpm: digitsOnly(e.target.value) }))}
                  placeholder={unlimitedPlaceholder}
                />
              </label>
              <label className="field">
                <span className="field-label">RPD</span>
                <input
                  className="field-control"
                  type="text"
                  inputMode="numeric"
                  pattern="[0-9]*"
                  value={createForm.rpd}
                  onChange={(e) => setCreateForm((prev) => ({ ...prev, rpd: digitsOnly(e.target.value) }))}
                  placeholder={unlimitedPlaceholder}
                />
              </label>
            </div>
          </div>
        </div>
      </Drawer>

      <Drawer
        open={Boolean(editingId && editForm)}
        wide
        title={localizedMessage(isZh, "apiKeys.editAPIKey")}
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
              {updateMut.isPending ? (localizedMessage(isZh, "common.saving")) : (localizedMessage(isZh, "common.save"))}
            </button>
          </>
        ) : undefined}
      >
        {editForm && (
          <div className="form-stack">
            <div>
              <p className="form-section-title">{localizedMessage(isZh, "apiKeys.text1BasicInformation")}</p>
              <div className="form-grid">
                <label className="field">
                  <span className="field-label">{localizedMessage(isZh, "common.name")}</span>
                  <input
                    className="field-control"
                    value={editForm.name}
                    onChange={(e) => setEditForm((prev) => (prev ? { ...prev, name: e.target.value } : prev))}
                  />
                </label>
                <label className="field">
                  <span className="field-label">{localizedMessage(isZh, "apiKeys.validity")}</span>
                  <input className="field-control" value={editForm.expires_text} disabled />
                </label>
                <label className="field">
                  <span className="field-label">{localizedMessage(isZh, "apiKeys.keyValue")}</span>
                  <span className="field-control secret-control is-disabled">
                    <input value={editForm.key} disabled />
                    <button
                      type="button"
                      className="secret-toggle"
                      data-tip={localizedMessage(isZh, "apiKeys.copyKey")}
                      aria-label={localizedMessage(isZh, "apiKeys.copyKey")}
                      onClick={async () => {
                        await navigator.clipboard.writeText(editForm.key);
                        setCopiedEditKey(true);
                        showToast(localizedMessage(isZh, "common.copied"));
                        setTimeout(() => setCopiedEditKey(false), 1200);
                      }}
                    >
                      <Copy />
                    </button>
                  </span>
                  {copiedEditKey && (
                    <span className="field-hint success">{localizedMessage(isZh, "common.copied")}</span>
                  )}
                </label>
              </div>
            </div>

            <div className="form-section">
              <p className="form-section-title">{localizedMessage(isZh, "apiKeys.text2AccessPermission")}</p>
              <ModelBindingField
                label={localizedMessage(isZh, "apiKeys.bindRoutesNoneDenyOnProtectedRoutes")}
                options={routeOptions}
                values={editForm.model_ids}
                placeholder={localizedMessage(isZh, "apiKeys.selectProtectedModelsThisKeyCanAccess")}
                emptyText={localizedMessage(isZh, "apiKeys.noMatchingRoutes")}
                onChange={(next) => setEditForm((prev) => (prev ? { ...prev, model_ids: next } : prev))}
              />
            </div>

            <div className="form-section">
              <p className="form-section-title">{localizedMessage(isZh, "apiKeys.text3AccessQuota")}</p>
              <div className="form-grid">
                <label className="field">
                  <span className="field-label">TPM</span>
                  <input
                    className="field-control"
                    type="text"
                    inputMode="numeric"
                    pattern="[0-9]*"
                    value={editForm.tpm}
                    onChange={(e) => setEditForm((prev) => (prev ? { ...prev, tpm: digitsOnly(e.target.value) } : prev))}
                  />
                </label>
                <label className="field">
                  <span className="field-label">TPD</span>
                  <input
                    className="field-control"
                    type="text"
                    inputMode="numeric"
                    pattern="[0-9]*"
                    value={editForm.tpd}
                    onChange={(e) => setEditForm((prev) => (prev ? { ...prev, tpd: digitsOnly(e.target.value) } : prev))}
                  />
                </label>
                <label className="field">
                  <span className="field-label">RPM</span>
                  <input
                    className="field-control"
                    type="text"
                    inputMode="numeric"
                    pattern="[0-9]*"
                    value={editForm.rpm}
                    onChange={(e) => setEditForm((prev) => (prev ? { ...prev, rpm: digitsOnly(e.target.value) } : prev))}
                  />
                </label>
                <label className="field">
                  <span className="field-label">RPD</span>
                  <input
                    className="field-control"
                    type="text"
                    inputMode="numeric"
                    pattern="[0-9]*"
                    value={editForm.rpd}
                    onChange={(e) => setEditForm((prev) => (prev ? { ...prev, rpd: digitsOnly(e.target.value) } : prev))}
                  />
                </label>
              </div>
            </div>
          </div>
        )}
      </Drawer>

      {/* Show the plaintext key only once after creation */}
      <Modal
        open={showCreatedDialog}
        title={localizedMessage(isZh, "apiKeys.apiKeyCreated")}
        description={localizedMessage(isZh, "apiKeys.copyAndSaveThisKeyNowItWillBeMaskedInLaterViews")}
        onClose={closeCreated}
        footer={(
          <>
            <button type="button" className="button button-secondary" onClick={closeCreated}>
              {localizedMessage(isZh, "common.close")}
            </button>
            <button
              type="button"
              className="button button-primary"
              onClick={async () => {
                if (!createdKey) return;
                await navigator.clipboard.writeText(createdKey);
                setCopiedCreatedKey(true);
                showToast(localizedMessage(isZh, "common.copied"));
              }}
            >
              {copiedCreatedKey ? (localizedMessage(isZh, "common.copied")) : (localizedMessage(isZh, "common.copy"))}
            </button>
          </>
        )}
      >
        <div className="reveal-key">
          <span className="field-control secret-control">
            <input readOnly value={createdKey ?? "-"} aria-label="API Key" />
          </span>
        </div>
      </Modal>

      <ConfirmDialog
        open={Boolean(apiKeyToDelete)}
        onOpenChange={(open) => {
          if (!open) setApiKeyToDelete(null);
        }}
        title={localizedMessage(isZh, "apiKeys.confirmAPIKeyDeletion")}
        description={
          apiKeyToDelete
            ? (localizedMessage(isZh, "common.thisActionCannotBeUndoneDelete", { a: apiKeyToDelete.name }))
            : undefined
        }
        cancelText={localizedMessage(isZh, "common.cancel")}
        confirmText={localizedMessage(isZh, "common.delete")}
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
        confirmText={localizedMessage(isZh, "common.ok")}
        onConfirm={() => setErrorDialog(null)}
      />
    </PageLayout>
  );
}
