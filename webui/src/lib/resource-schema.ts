/* ─── 资源 Schema: 定义每种资源的表单字段和列表列 ─── */

export type FieldType =
  | "text"
  | "number"
  | "select"
  | "multi-select" // 新增: 多选下拉/Checkbox
  | "path-list"
  | "tags"
  | "textarea"
  | "json"
  | "endpoints"
  | "plugins"
  | "credentials";

export interface FieldDef {
  key: string;
  label: string;
  type: FieldType;
  required?: boolean;
  placeholder?: string;
  options?: { label: string; value: string }[];
  defaultValue?: unknown;
  help?: string;
  advanced?: boolean; // 新增: 是否折叠到高级设置
}

export interface ColumnDef {
  key: string;
  label: string;
  render?: (value: unknown, item: Record<string, unknown>) => string;
}

export interface ResourceSchema {
  fields: FieldDef[];
  columns: ColumnDef[];
  defaultValues: Record<string, unknown>;
}

function joinArray(v: unknown): string {
  if (Array.isArray(v)) return v.join(", ");
  return String(v || "-");
}

function countArray(v: unknown): string {
  if (Array.isArray(v)) return String(v.length);
  if (v && typeof v === "object") return String(Object.keys(v).length);
  return "0";
}

export const SCHEMAS: Record<string, ResourceSchema> = {
  models: {
    fields: [
      { key: "name", label: "Name", type: "text", required: true, placeholder: "my-route" },
      { 
        key: "service", 
        label: "Service", 
        type: "select", 
        required: true, 
        placeholder: "Select a service",
        options: [] // 运行时动态填充
      },
      { key: "paths", label: "Paths", type: "path-list", required: true, placeholder: "/api/v1/*" },
      { 
        key: "methods", 
        label: "Methods", 
        type: "multi-select", 
        placeholder: "GET, POST...",
        options: [
          { label: "GET", value: "GET" },
          { label: "POST", value: "POST" },
          { label: "PUT", value: "PUT" },
          { label: "DELETE", value: "DELETE" },
          { label: "PATCH", value: "PATCH" },
          { label: "OPTIONS", value: "OPTIONS" },
          { label: "HEAD", value: "HEAD" },
        ],
        advanced: true // 虽然常用，但为了简化界面，也可以视为高级或默认全选？用户建议是折叠
      },
      {
        key: "match_type",
        label: "Match Type",
        type: "select",
        options: [
          { label: "auto", value: "" },
          { label: "exact", value: "exact" },
          { label: "prefix", value: "prefix" },
          { label: "param", value: "param" },
        ],
        advanced: true,
        help: "选择 auto 时不传该字段，由后端自动推导"
      },
      { key: "hosts", label: "Hosts", type: "path-list", placeholder: "api.example.com", advanced: true },
      { key: "priority", label: "Priority", type: "number", placeholder: "0", advanced: true },
      { key: "plugins", label: "Plugins", type: "plugins" },
    ],
    columns: [
      { key: "name", label: "Name" },
      { key: "service", label: "Service" },
      { key: "paths", label: "Paths", render: joinArray },
      { key: "hosts", label: "Hosts", render: joinArray },
      { key: "methods", label: "Methods", render: joinArray },
      { key: "plugins", label: "Plugins", render: countArray },
    ],
    defaultValues: {
      name: "",
      service: "",
      paths: [],
      methods: [],
      match_type: "",
      plugins: [],
    },
  },

  services: {
    fields: [
      { key: "name", label: "Name", type: "text", required: true, placeholder: "my-service" },
      { key: "url", label: "URL", type: "text", placeholder: "https://api.example.com", help: "与 backend 二选一" },
      { key: "backend", label: "Backend", type: "text", placeholder: "my-backend", help: "与 url 二选一" },
      { key: "provider", label: "Provider", type: "select", options: [
        { label: "无", value: "" },
        { label: "openai", value: "openai" },
        { label: "anthropic", value: "anthropic" },
        { label: "gemini", value: "gemini" },
        { label: "ollama", value: "ollama" },
      ]},
      { key: "scheme", label: "Scheme", type: "select", options: [
        { label: "自动", value: "" },
        { label: "http", value: "http" },
        { label: "https", value: "https" },
      ], advanced: true},
      { key: "plugins", label: "Plugins", type: "plugins", advanced: true },
    ],
    columns: [
      { key: "name", label: "Name" },
      { key: "url", label: "URL", render: (v) => String(v || "-") },
      { key: "backend", label: "Backend", render: (v) => String(v || "-") },
      { key: "provider", label: "Provider", render: (v) => String(v || "-") },
    ],
    defaultValues: {
      name: "",
      url: "",
      plugins: [],
    },
  },

  backends: {
    fields: [
      { key: "name", label: "Name", type: "text", required: true, placeholder: "my-backend" },
      { key: "algorithm", label: "Algorithm", type: "select", options: [
        { label: "roundrobin", value: "roundrobin" },
        { label: "chash", value: "chash" },
      ]},
      { key: "endpoints", label: "Endpoints", type: "endpoints" },
      { key: "retries", label: "Retries", type: "number", placeholder: "0", advanced: true },
    ],
    columns: [
      { key: "name", label: "Name" },
      { key: "algorithm", label: "Algorithm", render: (v) => String(v || "roundrobin") },
      { key: "endpoints", label: "Endpoints", render: countArray },
      { key: "retries", label: "Retries", render: (v) => String(v ?? "-") },
    ],
    defaultValues: {
      name: "",
      algorithm: "roundrobin",
      endpoints: [{ address: "", port: 80, weight: 1 }],
    },
  },

  consumers: {
    fields: [
      { key: "name", label: "Name", type: "text", required: true, placeholder: "my-consumer" },
      { key: "credentials", label: "Credentials", type: "credentials" },
      { key: "plugins", label: "Plugins", type: "plugins", advanced: true },
    ],
    columns: [
      { key: "name", label: "Name" },
      { key: "credentials", label: "Credentials", render: (v) => {
        if (v && typeof v === "object") return Object.keys(v as object).join(", ");
        return "-";
      }},
      { key: "plugins", label: "Plugins", render: countArray },
    ],
    defaultValues: {
      name: "",
      credentials: { "key-auth": { key: "" } },
      plugins: [],
    },
  },

  plugins: {
    fields: [
      { key: "name", label: "Name", type: "text", required: true, placeholder: "cors" },
      { key: "id", label: "Plugin ID", type: "text", placeholder: "与 name 相同可省略", advanced: true },
      { key: "config", label: "Config", type: "json" },
    ],
    columns: [
      { key: "name", label: "Name" },
      { key: "id", label: "Plugin ID", render: (v) => String(v || "-") },
      { key: "config", label: "Config", render: (v) => {
        if (!v || typeof v !== "object") return "-";
        const keys = Object.keys(v as object);
        return keys.length ? keys.join(", ") : "{}";
      }},
    ],
    defaultValues: {
      name: "",
      config: {},
    },
  },

  certificates: {
    fields: [
      { key: "name", label: "Name", type: "text", required: true, placeholder: "my-cert" },
      { key: "snis", label: "SNIs", type: "tags", required: true, placeholder: "example.com, *.example.com" },
      { key: "cert", label: "Certificate (PEM)", type: "textarea", placeholder: "-----BEGIN CERTIFICATE-----" },
      { key: "cert_file", label: "或 Cert 文件路径", type: "text", placeholder: "/path/to/cert.pem", help: "与 cert 二选一", advanced: true },
      { key: "key", label: "Private Key (PEM)", type: "textarea", placeholder: "-----BEGIN PRIVATE KEY-----" },
      { key: "key_file", label: "或 Key 文件路径", type: "text", placeholder: "/path/to/key.pem", help: "与 key 二选一", advanced: true },
    ],
    columns: [
      { key: "name", label: "Name" },
      { key: "snis", label: "SNIs", render: joinArray },
      { key: "cert_file", label: "Cert", render: (v, item) => String(v || (item.cert ? "PEM inline" : "-")) },
    ],
    defaultValues: {
      name: "",
      snis: [],
    },
  },
};
