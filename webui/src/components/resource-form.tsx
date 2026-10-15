import { useState, useMemo, type ReactNode } from "react";
import type { FieldDef } from "@/lib/resource-schema";
import { Plus, Trash2, ChevronDown, ChevronRight, Check } from "lucide-react";
import { cn } from "@/lib/utils";

interface ResourceFormProps {
  fields: FieldDef[];
  value: Record<string, unknown>;
  onChange: (value: Record<string, unknown>) => void;
  /** 编辑模式时 name 不可修改 */
  editingName?: string;
  extraContent?: ReactNode;
}

/* ─── 通用小控件 ─── */

function TagsInput({
  value,
  onChange,
  placeholder,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
}) {
  const [input, setInput] = useState("");

  const add = () => {
    const trimmed = input.trim();
    if (!trimmed) return;
    const newItems = trimmed.split(",").map((s) => s.trim()).filter(Boolean);
    // 去重添加
    const unique = [...new Set([...value, ...newItems])];
    onChange(unique);
    setInput("");
  };

  return (
    <div>
      <div className="flex flex-wrap gap-1.5 mb-1.5">
        {value.map((tag, i) => (
          <span
            key={`${tag}-${i}`}
            className="inline-flex items-center gap-1 rounded-md border border-slate-200 bg-slate-50 px-2 py-0.5 text-xs"
          >
            {tag}
            <button
              type="button"
              onClick={() => onChange(value.filter((_, j) => j !== i))}
              className="cursor-pointer text-slate-400 hover:text-rose-500"
            >
              &times;
            </button>
          </span>
        ))}
      </div>
      <div className="flex gap-1">
        <input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") { e.preventDefault(); add(); }
          }}
          placeholder={placeholder}
          className="flex-1 rounded-lg border border-slate-200 bg-white px-3 py-1.5 text-xs outline-none focus:border-slate-400"
        />
        <button
          type="button"
          onClick={add}
          className="cursor-pointer rounded-lg border border-slate-200 bg-white px-2 py-1 text-xs hover:bg-slate-50"
        >
          添加
        </button>
      </div>
    </div>
  );
}

function PathListEditor({
  value,
  onChange,
  placeholder,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
}) {
  const rows = value.length > 0 ? value : [""];

  const update = (idx: number, nextValue: string) => {
    const next = [...rows];
    next[idx] = nextValue;
    onChange(next);
  };

  const remove = (idx: number) => {
    const next = rows.filter((_, i) => i !== idx);
    onChange(next.length > 0 ? next : [""]);
  };

  return (
    <div className="space-y-2">
      {rows.map((path, idx) => (
        <div key={idx} className="flex items-center gap-2">
          <input
            value={path}
            onChange={(e) => update(idx, e.target.value)}
            placeholder={placeholder || "/v1/chat/completions"}
            className="flex-1 rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm outline-none focus:border-slate-400"
          />
          <button
            type="button"
            onClick={() => remove(idx)}
            className="cursor-pointer rounded-md border border-slate-200 bg-white px-2 py-1 text-xs text-slate-500 hover:bg-slate-50"
          >
            删除
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={() => onChange([...rows, ""])}
        className="flex cursor-pointer items-center gap-1 rounded-lg border border-dashed border-slate-300 px-3 py-1.5 text-xs text-slate-500 hover:border-slate-400 hover:text-slate-700"
      >
        <Plus className="h-3 w-3" /> 添加 Path
      </button>
    </div>
  );
}

function MultiSelect({
  value,
  onChange,
  options,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  options: { label: string; value: string }[];
}) {
  const toggle = (v: string) => {
    if (value.includes(v)) {
      onChange(value.filter((item) => item !== v));
    } else {
      onChange([...value, v]);
    }
  };

  return (
    <div className="flex flex-wrap gap-2">
      {options.map((opt) => {
        const selected = value.includes(opt.value);
        return (
          <button
            key={opt.value}
            type="button"
            onClick={() => toggle(opt.value)}
            className={cn(
              "flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs transition-colors",
              selected
                ? "border-slate-800 bg-slate-800 text-white"
                : "border-slate-200 bg-white text-slate-600 hover:border-slate-300"
            )}
          >
            {selected && <Check className="h-3 w-3" />}
            {opt.label}
          </button>
        );
      })}
    </div>
  );
}

function EndpointsEditor({
  value,
  onChange,
}: {
  value: Array<{ address: string; port: number; weight: number; headers?: Record<string, string> }>;
  onChange: (v: typeof value) => void;
}) {
  const update = (i: number, patch: Partial<typeof value[0]>) => {
    const next = [...value];
    next[i] = { ...next[i], ...patch };
    onChange(next);
  };

  return (
    <div className="space-y-2">
      {value.map((ep, i) => (
        <div key={i} className="flex items-center gap-2 rounded-lg border border-slate-200 bg-white p-2">
          <input
            value={ep.address}
            onChange={(e) => update(i, { address: e.target.value })}
            placeholder="127.0.0.1"
            className="flex-1 rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
          />
          <input
            type="number"
            value={ep.port}
            onChange={(e) => update(i, { port: Number(e.target.value) })}
            placeholder="80"
            className="w-20 rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
          />
          <input
            type="number"
            value={ep.weight}
            onChange={(e) => update(i, { weight: Number(e.target.value) })}
            placeholder="1"
            className="w-16 rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
          />
          <button
            type="button"
            onClick={() => onChange(value.filter((_, j) => j !== i))}
            className="cursor-pointer text-slate-400 hover:text-rose-500"
          >
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={() => onChange([...value, { address: "", port: 80, weight: 1 }])}
        className="flex cursor-pointer items-center gap-1 rounded-lg border border-dashed border-slate-300 px-3 py-1.5 text-xs text-slate-500 hover:border-slate-400 hover:text-slate-700"
      >
        <Plus className="h-3 w-3" /> 添加端点
      </button>
      {value.length > 0 && (
        <div className="text-[10px] text-slate-400">address | port | weight</div>
      )}
    </div>
  );
}

function PluginsEditor({
  value,
  onChange,
}: {
  value: Array<{ id?: string; name?: string; config?: Record<string, unknown> }>;
  onChange: (v: typeof value) => void;
}) {
  const [expandedIndex, setExpandedIndex] = useState<number | null>(null);

  const update = (i: number, patch: Partial<typeof value[0]>) => {
    const next = [...value];
    next[i] = { ...next[i], ...patch };
    onChange(next);
  };

  const updateConfig = (i: number, patch: Record<string, unknown>) => {
    const oldCfg = (value[i]?.config || {}) as Record<string, unknown>;
    const merged = { ...oldCfg, ...patch };
    const config: Record<string, unknown> = {};
    Object.entries(merged).forEach(([k, v]) => {
      if (v === "" || v === undefined || v === null) return;
      config[k] = v;
    });
    update(i, { config });
  };

  const hasAiProxy = value.some((p) => (p.id || p.name) === "ai-proxy");
  const hasKeyAuth = value.some((p) => (p.id || p.name) === "key-auth");
  const disableAdd = expandedIndex !== null;

  return (
    <div className="space-y-2">
      {value.map((p, i) => (
        <div key={i} className="rounded-lg border border-slate-200 bg-white p-2">
          <div className="mb-2 flex items-center justify-between gap-2">
            <button
              type="button"
              onClick={() => setExpandedIndex(expandedIndex === i ? null : i)}
              className="flex flex-1 items-center justify-between rounded-md border border-slate-200 bg-slate-50 px-2 py-1 text-xs font-medium text-slate-700"
            >
              <span>{p.id || p.name || "plugin"}</span>
              {expandedIndex === i ? (
                <ChevronDown className="h-3.5 w-3.5 text-slate-400" />
              ) : (
                <ChevronRight className="h-3.5 w-3.5 text-slate-400" />
              )}
            </button>
            <button
              type="button"
              onClick={() => {
                onChange(value.filter((_, j) => j !== i));
                setExpandedIndex((prev) => {
                  if (prev === null) return null;
                  if (prev === i) return null;
                  if (prev > i) return prev - 1;
                  return prev;
                });
              }}
              className="cursor-pointer text-slate-400 hover:text-rose-500"
            >
              <Trash2 className="h-3.5 w-3.5" />
            </button>
          </div>

          {expandedIndex === i && (
          <div className="flex-1 space-y-1">
            {(p.id === "ai-proxy" || p.name === "ai-proxy") && (
              <div className="space-y-2">
                <div className="grid grid-cols-2 gap-2">
                  <select
                    value={String((p.config as Record<string, unknown>)?.from ?? "auto")}
                    onChange={(e) => updateConfig(i, { from: e.target.value })}
                    className="rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
                  >
                    <option value="auto">from (auto)</option>
                    <option value="openai">openai</option>
                    <option value="anthropic">anthropic</option>
                    <option value="gemini">gemini</option>
                    <option value="ollama">ollama</option>
                  </select>
                  <select
                    value={String((p.config as Record<string, unknown>)?.to ?? "auto")}
                    onChange={(e) => updateConfig(i, { to: e.target.value })}
                    className="rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
                  >
                    <option value="auto">to (auto)</option>
                    <option value="openai">openai</option>
                    <option value="anthropic">anthropic</option>
                    <option value="gemini">gemini</option>
                    <option value="ollama">ollama</option>
                  </select>
                </div>

                <details className="rounded-md border border-slate-200 bg-slate-50 p-2">
                  <summary className="cursor-pointer text-xs text-slate-600">更多可选配置</summary>
                  <div className="mt-2 grid grid-cols-2 gap-2">
                    <input
                      value={String((p.config as Record<string, unknown>)?.api_key ?? "")}
                      onChange={(e) => updateConfig(i, { api_key: e.target.value })}
                      placeholder="api_key"
                      className="col-span-2 rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
                    />
                    <input
                      value={String((p.config as Record<string, unknown>)?.model ?? "")}
                      onChange={(e) => updateConfig(i, { model: e.target.value })}
                      placeholder="model"
                      className="col-span-2 rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
                    />
                    <input
                      type="number"
                      value={String((p.config as Record<string, unknown>)?.max_tokens ?? "")}
                      onChange={(e) =>
                        updateConfig(i, {
                          max_tokens: e.target.value ? Number(e.target.value) : "",
                        })}
                      placeholder="max_tokens"
                      className="rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
                    />
                    <input
                      type="number"
                      step="0.1"
                      value={String((p.config as Record<string, unknown>)?.temperature ?? "")}
                      onChange={(e) =>
                        updateConfig(i, {
                          temperature: e.target.value ? Number(e.target.value) : "",
                        })}
                      placeholder="temperature"
                      className="rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
                    />
                  </div>
                </details>
              </div>
            )}

            {(p.id === "key-auth" || p.name === "key-auth") && (
              <div className="space-y-2">
                <select
                  value={String((p.config as Record<string, unknown>)?.key_in ?? "auto")}
                  onChange={(e) => updateConfig(i, { key_in: e.target.value })}
                  className="rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
                >
                  <option value="auto">key_in (auto)</option>
                  <option value="header">header</option>
                  <option value="query">query</option>
                </select>
                <details className="rounded-md border border-slate-200 bg-slate-50 p-2">
                  <summary className="cursor-pointer text-xs text-slate-600">更多可选配置</summary>
                  <div className="mt-2 grid grid-cols-2 gap-2">
                    <input
                      value={String((p.config as Record<string, unknown>)?.key_name ?? "")}
                      onChange={(e) => updateConfig(i, { key_name: e.target.value })}
                      placeholder="key_name"
                      className="col-span-2 rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
                    />
                    <label className="col-span-2 flex items-center gap-2 rounded-md border border-slate-200 px-2 py-1 text-xs text-slate-600">
                      <input
                        type="checkbox"
                        checked={Boolean((p.config as Record<string, unknown>)?.hide_credentials)}
                        onChange={(e) => updateConfig(i, { hide_credentials: e.target.checked ? true : "" })}
                      />
                      hide_credentials
                    </label>
                  </div>
                </details>
              </div>
            )}
          </div>
          )}
        </div>
      ))}
      <div className="flex gap-2">
        <button
          type="button"
          onClick={() => {
            if (disableAdd || hasAiProxy) return;
            onChange([...value, { id: "ai-proxy", config: {} }]);
          }}
          disabled={disableAdd || hasAiProxy}
          className={cn(
            "flex items-center gap-1 rounded-lg border border-dashed px-3 py-1.5 text-xs",
            disableAdd || hasAiProxy
              ? "cursor-not-allowed border-slate-200 text-slate-300"
              : "cursor-pointer border-slate-300 text-slate-500 hover:border-slate-400 hover:text-slate-700"
          )}
        >
          <Plus className="h-3 w-3" /> 添加 ai-proxy
        </button>
        <button
          type="button"
          onClick={() => {
            if (disableAdd || hasKeyAuth) return;
            onChange([...value, { id: "key-auth", config: {} }]);
          }}
          disabled={disableAdd || hasKeyAuth}
          className={cn(
            "flex items-center gap-1 rounded-lg border border-dashed px-3 py-1.5 text-xs",
            disableAdd || hasKeyAuth
              ? "cursor-not-allowed border-slate-200 text-slate-300"
              : "cursor-pointer border-slate-300 text-slate-500 hover:border-slate-400 hover:text-slate-700"
          )}
        >
          <Plus className="h-3 w-3" /> 添加 key-auth
        </button>
      </div>
    </div>
  );
}

function CredentialsEditor({
  value,
  onChange,
}: {
  value: Record<string, unknown>;
  onChange: (v: Record<string, unknown>) => void;
}) {
  const setEnabled = (kind: "key-auth" | "basic-auth" | "jwt-auth", enabled: boolean) => {
    const next = { ...value } as Record<string, unknown>;
    if (!enabled) {
      delete next[kind];
      onChange(next);
      return;
    }
    if (kind === "key-auth") next[kind] = { key: "" };
    if (kind === "basic-auth") next[kind] = { username: "", password: "" };
    if (kind === "jwt-auth") next[kind] = { key: "", secret: "" };
    onChange(next);
  };

  const setField = (
    kind: "key-auth" | "basic-auth" | "jwt-auth",
    field: string,
    fieldValue: string,
  ) => {
    const current = ((value[kind] as Record<string, unknown>) || {}) as Record<string, unknown>;
    onChange({
      ...value,
      [kind]: {
        ...current,
        [field]: fieldValue,
      },
    });
  };

  const generateKeyAuthKey = () => {
    const suffix =
      typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
        ? crypto.randomUUID().replace(/-/g, "")
        : `${Date.now()}${Math.random().toString(16).slice(2)}`;
    setField("key-auth", "key", `sk-${suffix.slice(0, 32)}`);
  };

  const keyAuth = ((value["key-auth"] as Record<string, unknown>) || {}) as Record<string, unknown>;
  const basicAuth = ((value["basic-auth"] as Record<string, unknown>) || {}) as Record<string, unknown>;
  const jwtAuth = ((value["jwt-auth"] as Record<string, unknown>) || {}) as Record<string, unknown>;

  return (
    <div className="space-y-3">
      <div className="rounded-lg border border-slate-200 bg-white p-3">
        <label className="mb-2 flex items-center gap-2 text-xs font-medium text-slate-700">
          <input
            type="checkbox"
            checked={Boolean(value["key-auth"])}
            onChange={(e) => setEnabled("key-auth", e.target.checked)}
          />
          key-auth
        </label>
        {Boolean(value["key-auth"]) && (
          <div className="flex items-center gap-2">
            <input
              value={String(keyAuth.key ?? "")}
              onChange={(e) => setField("key-auth", "key", e.target.value)}
              placeholder="api key"
              className="flex-1 rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
            />
            <button
              type="button"
              onClick={generateKeyAuthKey}
              className="rounded-md border border-slate-200 bg-slate-50 px-2 py-1 text-xs text-slate-600 hover:bg-slate-100"
            >
              自动生成
            </button>
          </div>
        )}
      </div>

      <div className="rounded-lg border border-slate-200 bg-white p-3">
        <label className="mb-2 flex items-center gap-2 text-xs font-medium text-slate-700">
          <input
            type="checkbox"
            checked={Boolean(value["basic-auth"])}
            onChange={(e) => setEnabled("basic-auth", e.target.checked)}
          />
          basic-auth
        </label>
        {Boolean(value["basic-auth"]) && (
          <div className="grid grid-cols-2 gap-2">
            <input
              value={String(basicAuth.username ?? "")}
              onChange={(e) => setField("basic-auth", "username", e.target.value)}
              placeholder="username"
              className="rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
            />
            <input
              value={String(basicAuth.password ?? "")}
              onChange={(e) => setField("basic-auth", "password", e.target.value)}
              placeholder="password"
              className="rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
            />
          </div>
        )}
      </div>

      <div className="rounded-lg border border-slate-200 bg-white p-3">
        <label className="mb-2 flex items-center gap-2 text-xs font-medium text-slate-700">
          <input
            type="checkbox"
            checked={Boolean(value["jwt-auth"])}
            onChange={(e) => setEnabled("jwt-auth", e.target.checked)}
          />
          jwt-auth
        </label>
        {Boolean(value["jwt-auth"]) && (
          <div className="grid grid-cols-2 gap-2">
            <input
              value={String(jwtAuth.key ?? "")}
              onChange={(e) => setField("jwt-auth", "key", e.target.value)}
              placeholder="key"
              className="rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
            />
            <input
              value={String(jwtAuth.secret ?? "")}
              onChange={(e) => setField("jwt-auth", "secret", e.target.value)}
              placeholder="secret"
              className="rounded-md border border-slate-200 px-2 py-1 text-xs outline-none focus:border-slate-400"
            />
          </div>
        )}
      </div>
    </div>
  );
}

/* ─── 字段渲染器 ─── */
function FieldRenderer({
  field,
  value,
  onChange,
  disabled = false,
}: {
  field: FieldDef;
  value: unknown;
  onChange: (v: unknown) => void;
  disabled?: boolean;
}) {
  return (
    <div className="mb-4">
      <label className="mb-1.5 flex items-center gap-1.5 text-xs font-medium text-slate-600">
        {field.label}
        {field.required && <span className="text-rose-500">*</span>}
      </label>
      {field.help && (
        <p className="mb-1.5 text-[11px] text-slate-400">{field.help}</p>
      )}

      {field.type === "text" && (
        <input
          value={String(value ?? "")}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
          placeholder={field.placeholder}
          className="w-full rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm outline-none focus:border-slate-400 disabled:opacity-60"
        />
      )}

      {field.type === "number" && (
        <input
          type="number"
          value={value !== undefined ? String(value) : ""}
          onChange={(e) => onChange(e.target.value ? Number(e.target.value) : undefined)}
          placeholder={field.placeholder}
          className="w-full rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm outline-none focus:border-slate-400"
        />
      )}

      {field.type === "select" && (
        <div className="relative">
          <select
            value={String(value ?? "")}
            onChange={(e) => onChange(e.target.value || undefined)}
            className="w-full appearance-none rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm outline-none focus:border-slate-400"
          >
            {!field.options?.some(o => o.value === "") && <option value="">Select...</option>}
            {(field.options || []).map((opt) => (
              <option key={opt.value} value={opt.value}>
                {opt.label}
              </option>
            ))}
          </select>
          <ChevronDown className="pointer-events-none absolute right-3 top-2.5 h-4 w-4 text-slate-400" />
        </div>
      )}

      {field.type === "multi-select" && (
        <MultiSelect
          value={Array.isArray(value) ? (value as string[]) : []}
          onChange={onChange}
          options={field.options || []}
        />
      )}

      {field.type === "path-list" && (
        <PathListEditor
          value={Array.isArray(value) ? (value as string[]) : []}
          onChange={(v) => onChange(v)}
          placeholder={field.placeholder}
        />
      )}

      {field.type === "tags" && (
        <TagsInput
          value={Array.isArray(value) ? (value as string[]) : []}
          onChange={(v) => onChange(v)}
          placeholder={field.placeholder}
        />
      )}

      {field.type === "textarea" && (
        <textarea
          value={String(value ?? "")}
          onChange={(e) => onChange(e.target.value)}
          rows={4}
          placeholder={field.placeholder}
          className="w-full rounded-lg border border-slate-200 bg-white px-3 py-2 font-mono text-xs outline-none focus:border-slate-400"
        />
      )}

      {field.type === "json" && (
        <textarea
          value={typeof value === "object"
            ? JSON.stringify(value, null, 2)
            : String(value ?? "{}")}
          onChange={(e) => {
            try { onChange(JSON.parse(e.target.value)); } catch { /* ignore */ }
          }}
          rows={6}
          className="w-full rounded-lg border border-slate-200 bg-white px-3 py-2 font-mono text-xs outline-none focus:border-slate-400"
        />
      )}

      {field.type === "endpoints" && (
        <EndpointsEditor
          value={Array.isArray(value) ? (value as Array<{ address: string; port: number; weight: number }>) : []}
          onChange={(v) => onChange(v)}
        />
      )}

      {field.type === "plugins" && (
        <PluginsEditor
          value={Array.isArray(value) ? (value as Array<{ id?: string; name?: string; config?: Record<string, unknown> }>) : []}
          onChange={(v) => onChange(v)}
        />
      )}

      {field.type === "credentials" && (
        <CredentialsEditor
          value={(value as Record<string, unknown>) || {}}
          onChange={(v) => onChange(v)}
        />
      )}
    </div>
  );
}

/* ─── 主表单 ─── */

export function ResourceForm({ fields, value, onChange, editingName, extraContent }: ResourceFormProps) {
  const [showAdvanced, setShowAdvanced] = useState(false);

  const set = (key: string, v: unknown) => {
    onChange({ ...value, [key]: v });
  };

  const basicFields = useMemo(() => fields.filter((f) => !f.advanced), [fields]);
  const advancedFields = useMemo(() => fields.filter((f) => f.advanced), [fields]);

  return (
    <div>
      {extraContent && <div className="mb-6">{extraContent}</div>}
      
      <div className="space-y-1">
        {basicFields.map((field) => (
          <FieldRenderer
            key={field.key}
            field={field}
            value={value[field.key]}
            onChange={(v) => set(field.key, v)}
            disabled={field.key === "name" && !!editingName}
          />
        ))}
      </div>

      {advancedFields.length > 0 && (
        <div className="mt-6 border-t border-slate-100 pt-4">
          <button
            type="button"
            onClick={() => setShowAdvanced(!showAdvanced)}
            className="flex w-full items-center justify-between rounded-lg bg-slate-50 px-3 py-2 text-xs font-medium text-slate-600 hover:bg-slate-100"
          >
            <span>高级设置 ({advancedFields.length})</span>
            {showAdvanced ? (
              <ChevronDown className="h-4 w-4 text-slate-400" />
            ) : (
              <ChevronRight className="h-4 w-4 text-slate-400" />
            )}
          </button>
          
          {showAdvanced && (
            <div className="mt-4 space-y-1 animate-in fade-in slide-in-from-top-2 duration-200">
              {advancedFields.map((field) => (
                <FieldRenderer
                  key={field.key}
                  field={field}
                  value={value[field.key]}
                  onChange={(v) => set(field.key, v)}
                  disabled={field.key === "name" && !!editingName}
                />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
