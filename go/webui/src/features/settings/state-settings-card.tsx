import { useMutation, useQueries, useQueryClient } from "@tanstack/react-query";
import { Loader2, RefreshCw, Save, TriangleAlert } from "lucide-react";
import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { backend } from "@/lib/backend";
import { localizedMessage, type MessageKey } from "@/lib/messages";
import {
  sameStateSettings,
  stateSettingsFromValues,
  stateSettingsPayload,
  validateStateSettings,
  type StateSettingsDraft,
} from "@/lib/state-settings";
import { SettingsFormSurface } from "./settings-form-surface";

const STATE_TYPE_KEY = "state.type";
const STATE_URL_KEY = "state.url";

type ShowSettingsError = (titleKey: MessageKey, error: unknown) => void;

export function StateSettingsCard({
  isZh,
  onError,
  builtInRedisURL,
}: {
  isZh: boolean;
  onError: ShowSettingsError;
  builtInRedisURL: string | null;
}) {
  const queries = useQueries({
    queries: [STATE_TYPE_KEY, STATE_URL_KEY].map((key) => ({
      queryKey: ["setting", key],
      queryFn: () => backend<string | null>("get_setting", { key }),
    })),
  });

  if (queries.some((query) => query.isError)) {
    return (
      <SettingsFormSurface
        title={localizedMessage(isZh, "v2.settings.stateStorage")}
        description={localizedMessage(isZh, "v2.settings.stateStorageDescription")}
      >
        <div className="space-y-3">
          <p className="text-sm text-red-600">
            {localizedMessage(isZh, "v2.settings.stateLoadError")}
          </p>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            className="flex items-center gap-1.5"
            onClick={() => queries.forEach((query) => { void query.refetch(); })}
          >
            <RefreshCw className="h-3.5 w-3.5" />
            {localizedMessage(isZh, "common.refresh")}
          </Button>
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
        <Loader2 className="h-4 w-4 animate-spin text-slate-400" />
      </SettingsFormSurface>
    );
  }

  const baseline = stateSettingsFromValues(queries[0].data ?? null, queries[1].data ?? null);
  return (
    <StateSettingsForm
      key={`${baseline.type}\u0000${baseline.url}`}
      baseline={baseline}
      isZh={isZh}
      onError={onError}
      builtInRedisURL={builtInRedisURL}
    />
  );
}

function StateSettingsForm({
  baseline,
  isZh,
  onError,
  builtInRedisURL,
}: {
  baseline: StateSettingsDraft;
  isZh: boolean;
  onError: ShowSettingsError;
  builtInRedisURL: string | null;
}) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<StateSettingsDraft>(baseline);
  const invalid = validateStateSettings(draft) !== null;
  const dirty = !sameStateSettings(draft, baseline);
  const saveMutation = useMutation({
    mutationFn: (values: Record<string, string>) => backend<Record<string, string>>("set_settings", { values }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["setting", STATE_TYPE_KEY] });
      queryClient.invalidateQueries({ queryKey: ["setting", STATE_URL_KEY] });
    },
    onError: (error: unknown) => onError("settings.error.state", error),
  });

  return (
    <SettingsFormSurface
      title={localizedMessage(isZh, "v2.settings.stateStorage")}
      description={localizedMessage(isZh, "v2.settings.stateStorageDescription")}
      badge={<span className="v2-setting-badge">Gateway</span>}
    >
      <div className="v2-setting-stack">
        <div className="space-y-1.5">
          <label className="ml-1 text-xs text-slate-700">
            {localizedMessage(isZh, "v2.settings.stateBackend")}
          </label>
          <Select
            value={draft.type}
            disabled={saveMutation.isPending}
            onValueChange={(value) => setDraft((current) => ({
              ...current,
              type: value === "redis" ? "redis" : "memory",
            }))}
          >
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="memory">{localizedMessage(isZh, "v2.settings.memory")}</SelectItem>
              <SelectItem value="redis">Redis</SelectItem>
            </SelectContent>
          </Select>
        </div>

        {draft.type === "redis" && (
          <>
            <StateURLField
              isZh={isZh}
              value={draft.url}
              invalid={invalid}
              disabled={saveMutation.isPending}
              builtInRedisURL={builtInRedisURL}
              onChange={(url) => setDraft((current) => ({ ...current, url }))}
            />
            {!builtInRedisURL && (
              <p className="text-xs text-slate-500">
                {localizedMessage(isZh, "v2.settings.builtInRedisUnavailable")}
              </p>
            )}
            <p className="text-xs text-slate-500">
              {localizedMessage(isZh, "v2.settings.builtInRedisAddressNote")}
            </p>
            <p className="flex items-start gap-1.5 text-xs text-amber-700">
              <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              {localizedMessage(isZh, "v2.settings.redisPlaintextWarning")}
            </p>
          </>
        )}

        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            className="flex items-center gap-1.5"
            disabled={saveMutation.isPending || !dirty || invalid}
            onClick={() => saveMutation.mutate(stateSettingsPayload(draft))}
          >
            {saveMutation.isPending
              ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
              : <Save className="h-3.5 w-3.5" />}
            {localizedMessage(isZh, "v2.api-keys.save")}
          </Button>
          <p className="text-xs text-slate-500">
            {localizedMessage(isZh, "v2.settings.statePublishNote")}
          </p>
        </div>
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
    <div className="space-y-1.5">
      <label className="ml-1 text-xs text-slate-700">
        {localizedMessage(isZh, "v2.settings.redisUrl")}
      </label>
      <div className="flex items-center gap-2">
        <Input
          type="text"
          value={value}
          placeholder="redis://user:password@redis.internal:6379/0"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          className={`min-w-0 flex-1${invalid ? " border-red-400 focus-visible:ring-red-400" : ""}`}
          aria-invalid={invalid}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value)}
        />
        <Button
          type="button"
          variant="secondary"
          size="sm"
          className="whitespace-nowrap"
          aria-label={localizedMessage(isZh, "v2.settings.useBuiltIn")}
          disabled={disabled || !builtInRedisURL}
          onClick={() => { if (builtInRedisURL) onChange(builtInRedisURL); }}
        >
          {localizedMessage(isZh, "v2.settings.useBuiltIn")}
        </Button>
      </div>
      {invalid && (
        <p className="text-xs text-red-600">
          {localizedMessage(isZh, "v2.settings.invalidRedisUrl")}
        </p>
      )}
    </div>
  );
}
