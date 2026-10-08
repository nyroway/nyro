import { useMutation, useQueries, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { Notice } from "@/components/v2/notice";
import { settingsApi } from "@/lib/api/settings";
import { localizedMessage } from "@/lib/messages";
import {
  sameStateSettings,
  stateSettingsFromValues,
  stateSettingsPayload,
  validateStateSettings,
  type StateSettingsDraft,
} from "@/lib/state-settings";
import { reportSettingsError, reportSettingsSaved } from "./settings-feedback";
import { SettingsFormSurface } from "./settings-form-surface";

const STATE_TYPE_KEY = "state.type";
const STATE_URL_KEY = "state.url";

export function StateSettingsCard({
  isZh,
  builtInRedisURL,
}: {
  isZh: boolean;
  builtInRedisURL: string | null;
}) {
  const queries = useQueries({
    queries: [STATE_TYPE_KEY, STATE_URL_KEY].map((key) => ({
      queryKey: ["setting", key],
      queryFn: () => settingsApi.get(key),
    })),
  });

  if (queries.some((query) => query.isError)) {
    return (
      <SettingsFormSurface
        title={localizedMessage(isZh, "v2.settings.stateStorage")}
        description={localizedMessage(isZh, "v2.settings.stateStorageDescription")}
      >
        <Notice tone="danger">
          {localizedMessage(isZh, "v2.settings.stateLoadError")}
        </Notice>
        <div className="config-actions">
          <button
            type="button"
            className="button button-secondary button-sm"
            onClick={() => queries.forEach((query) => { void query.refetch(); })}
          >
            {localizedMessage(isZh, "common.refresh")}
          </button>
        </div>
      </SettingsFormSurface>
    );
  }

  if (queries.some((query) => query.isPending || query.isFetching)) {
    return (
      <SettingsFormSurface
        title={localizedMessage(isZh, "v2.settings.stateStorage")}
        description={localizedMessage(isZh, "v2.settings.stateStorageDescription")}
      >
        <div className="config-actions">
          <span className="spinner" aria-label={localizedMessage(isZh, "common.loading")} />
        </div>
      </SettingsFormSurface>
    );
  }

  const baseline = stateSettingsFromValues(queries[0].data ?? null, queries[1].data ?? null);
  return (
    <StateSettingsForm
      key={`${baseline.type}\u0000${baseline.url}`}
      baseline={baseline}
      isZh={isZh}
      builtInRedisURL={builtInRedisURL}
    />
  );
}

function StateSettingsForm({
  baseline,
  isZh,
  builtInRedisURL,
}: {
  baseline: StateSettingsDraft;
  isZh: boolean;
  builtInRedisURL: string | null;
}) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<StateSettingsDraft>(baseline);
  const invalid = validateStateSettings(draft) !== null;
  const dirty = !sameStateSettings(draft, baseline);
  const saveMutation = useMutation({
    // state.type/state.url 必须同批提交（PUT /settings，§8.4）
    mutationFn: (values: Record<string, string>) => settingsApi.setBulk(values),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["setting", STATE_TYPE_KEY] });
      queryClient.invalidateQueries({ queryKey: ["setting", STATE_URL_KEY] });
      reportSettingsSaved(isZh);
    },
    onError: (error: unknown) => reportSettingsError(isZh, "settings.error.state", error),
  });

  return (
    <SettingsFormSurface
      title={localizedMessage(isZh, "v2.settings.stateStorage")}
      description={localizedMessage(isZh, "v2.settings.stateStorageDescription")}
      badge={<span className="tag">Gateway</span>}
    >
      <div className="form-grid">
        <div className="field full">
          <span className="field-label">{localizedMessage(isZh, "v2.settings.stateBackend")}</span>
          <div className="radio-set">
            <button
              type="button"
              className={`radio-card${draft.type === "memory" ? " selected" : ""}`}
              disabled={saveMutation.isPending}
              onClick={() => setDraft((current) => ({ ...current, type: "memory" }))}
            >
              <span className="radio-circle" aria-hidden="true" />
              <span className="radio-text"><strong>{localizedMessage(isZh, "v2.settings.memory")}</strong></span>
            </button>
            <button
              type="button"
              className={`radio-card${draft.type === "redis" ? " selected" : ""}`}
              disabled={saveMutation.isPending}
              onClick={() => setDraft((current) => ({ ...current, type: "redis" }))}
            >
              <span className="radio-circle" aria-hidden="true" />
              <span className="radio-text"><strong>Redis</strong></span>
            </button>
          </div>
        </div>

        {draft.type === "redis" && (
          <StateURLField
            isZh={isZh}
            value={draft.url}
            invalid={invalid}
            disabled={saveMutation.isPending}
            builtInRedisURL={builtInRedisURL}
            onChange={(url) => setDraft((current) => ({ ...current, url }))}
          />
        )}
      </div>

      {draft.type === "redis" && (
        <Notice tone="warning">
          {localizedMessage(isZh, "v2.settings.redisPlaintextWarning")}
        </Notice>
      )}

      <div className="config-actions">
        <span className="config-note">{localizedMessage(isZh, "v2.settings.statePublishNote")}</span>
        <button
          type="button"
          className="button button-primary button-sm"
          disabled={saveMutation.isPending || !dirty || invalid}
          onClick={() => saveMutation.mutate(stateSettingsPayload(draft))}
        >
          {saveMutation.isPending && <span className="spinner" aria-hidden="true" />}
          {localizedMessage(isZh, "v2.api-keys.save")}
        </button>
      </div>
    </SettingsFormSurface>
  );
}

export function StateURLField({
  isZh,
  value,
  invalid,
  disabled,
  builtInRedisURL,
  onChange,
}: {
  isZh: boolean;
  value: string;
  invalid: boolean;
  disabled: boolean;
  builtInRedisURL: string | null;
  onChange: (value: string) => void;
}) {
  return (
    <div className="field full">
      <label className="field-label">{localizedMessage(isZh, "v2.settings.redisUrl")}</label>
      <div className={`field-control${invalid ? " is-error" : ""}`}>
        <input
          type="text"
          value={value}
          placeholder="redis://user:password@redis.internal:6379/0"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          aria-invalid={invalid}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value)}
        />
        <button
          type="button"
          className="button button-text button-sm"
          aria-label={localizedMessage(isZh, "v2.settings.useBuiltIn")}
          disabled={disabled || !builtInRedisURL}
          onClick={() => { if (builtInRedisURL) onChange(builtInRedisURL); }}
        >
          {localizedMessage(isZh, "v2.settings.useBuiltIn")}
        </button>
      </div>
      {invalid && (
        <span className="field-hint error">
          {localizedMessage(isZh, "v2.settings.invalidRedisUrl")}
        </span>
      )}
      {!builtInRedisURL && (
        <span className="field-hint">
          {localizedMessage(isZh, "v2.settings.builtInRedisUnavailable")}
        </span>
      )}
      <span className="field-hint">
        {localizedMessage(isZh, "v2.settings.builtInRedisAddressNote")}
      </span>
    </div>
  );
}
