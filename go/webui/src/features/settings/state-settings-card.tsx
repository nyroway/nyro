import { useMutation, useQueries, useQueryClient } from "@tanstack/react-query";
import { Eye, EyeOff, Loader2, Save, TriangleAlert } from "lucide-react";
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
}: {
  isZh: boolean;
  onError: ShowSettingsError;
}) {
  const queries = useQueries({
    queries: [STATE_TYPE_KEY, STATE_URL_KEY].map((key) => ({
      queryKey: ["setting", key],
      queryFn: () => backend<string | null>("get_setting", { key }),
    })),
  });

  if (queries.some((query) => query.isPending)) {
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
    />
  );
}

function StateSettingsForm({
  baseline,
  isZh,
  onError,
}: {
  baseline: StateSettingsDraft;
  isZh: boolean;
  onError: ShowSettingsError;
}) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<StateSettingsDraft>(baseline);
  const [reveal, setReveal] = useState(false);
  const invalid = validateStateSettings(draft) !== null;
  const dirty = !sameStateSettings(draft, baseline);
  const saveMutation = useMutation({
    mutationFn: () => backend<Record<string, string>>("set_settings", { values: stateSettingsPayload(draft) }),
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
              reveal={reveal}
              invalid={invalid}
              onChange={(url) => setDraft((current) => ({ ...current, url }))}
              onToggleReveal={() => setReveal((current) => !current)}
            />
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
            onClick={() => saveMutation.mutate()}
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
  reveal,
  invalid,
  onChange,
  onToggleReveal,
}: {
  isZh: boolean;
  value: string;
  reveal: boolean;
  invalid: boolean;
  onChange: (value: string) => void;
  onToggleReveal: () => void;
}) {
  return (
    <div className="space-y-1.5">
      <label className="ml-1 text-xs text-slate-700">
        {localizedMessage(isZh, "v2.settings.redisUrl")}
      </label>
      <div className="relative">
        <Input
          type={reveal ? "text" : "password"}
          value={value}
          placeholder="redis://user:password@redis.internal:6379/0"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          className={`pr-10${invalid ? " border-red-400 focus-visible:ring-red-400" : ""}`}
          aria-invalid={invalid}
          onChange={(event) => onChange(event.target.value)}
        />
        <button
          type="button"
          className="absolute top-1/2 right-3 -translate-y-1/2 cursor-pointer text-slate-400 hover:text-slate-600"
          aria-label={localizedMessage(isZh, reveal ? "v2.providers.hide" : "v2.providers.show")}
          onClick={onToggleReveal}
        >
          {reveal ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
        </button>
      </div>
      {invalid && (
        <p className="text-xs text-red-600">
          {localizedMessage(isZh, "v2.settings.invalidRedisUrl")}
        </p>
      )}
    </div>
  );
}
