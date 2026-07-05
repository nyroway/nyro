export interface GoUpstream {
  id: string;
  name: string;
  protocol?: string;
  base_url?: string;
  credentials?: Record<string, unknown> | string | null;
  models?: Record<string, unknown> | string | null;
  proxy_url?: string;
  enabled: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface GoCreateUpstream {
  name: string;
  protocol?: string;
  base_url?: string;
  credentials?: Record<string, unknown>;
  models?: Record<string, unknown>;
  proxy_url?: string;
  enabled?: boolean;
}

export interface GoUpdateUpstream {
  name?: string;
  protocol?: string;
  base_url?: string;
  credentials?: Record<string, unknown>;
  models?: Record<string, unknown>;
  proxy_url?: string;
  enabled?: boolean;
}

export interface GoRouteUpstream {
  id: string;
  route_id: string;
  upstream_id: string;
  model: string;
  weight: number;
  priority: number;
  enabled: boolean;
  created_at?: string;
}

export interface GoRoute {
  id: string;
  model: string;
  balance: "weighted" | "priority";
  enable_auth: boolean;
  enable_payload?: boolean | null;
  enabled: boolean;
  upstreams?: GoRouteUpstream[];
  created_at?: string;
  updated_at?: string;
}

export interface GoCreateRouteUpstream {
  upstream_id: string;
  model: string;
  weight?: number;
  priority?: number;
  enabled?: boolean;
}

export interface GoCreateRoute {
  model: string;
  balance?: "weighted" | "priority";
  enable_auth?: boolean;
  enable_payload?: boolean | null;
  upstreams: GoCreateRouteUpstream[];
}

export interface GoUpdateRoute {
  model?: string;
  balance?: "weighted" | "priority";
  enable_auth?: boolean;
  enable_payload?: boolean | null;
  enabled?: boolean;
  upstreams?: GoCreateRouteUpstream[];
}

export interface GoConsumerKey {
  id: string;
  consumer_id: string;
  name: string;
  key_prefix: string;
  token?: string;
  enabled: boolean;
  expires_at?: string;
  last_used_at?: string;
  created_at?: string;
  updated_at?: string;
}

export interface GoConsumerQuota {
  id?: string;
  consumer_id?: string;
  quota_type: "requests" | "tokens" | "concurrency" | string;
  quota_limit: number;
  window?: string;
}

export interface GoConsumer {
  id: string;
  name: string;
  enabled: boolean;
  keys?: GoConsumerKey[];
  routes?: string[];
  quotas?: GoConsumerQuota[];
  created_at?: string;
  updated_at?: string;
}

export interface GoCreateConsumerKey {
  name: string;
  token?: string;
  expires_at?: string;
  enabled?: boolean;
}

export interface GoCreateConsumerQuota {
  quota_type: "requests" | "tokens" | "concurrency" | string;
  quota_limit: number;
  window?: string;
}

export interface GoCreateConsumer {
  name: string;
  enabled?: boolean;
  keys?: GoCreateConsumerKey[];
  routes?: string[];
  quotas?: GoCreateConsumerQuota[];
}

// The current Go admin API only supports name/enabled updates for consumers.
export interface GoUpdateConsumer {
  name?: string;
  enabled?: boolean;
}

export interface GoProviderPreset {
  id: string;
  name: string;
  default_protocol: string;
  default_model?: string;
  protocols: Array<{ id: string; base_url?: string }>;
  credentials: {
    fields: Array<{
      name: string;
      type: string;
      required: boolean;
      default?: string;
      values?: string[];
      env?: string;
      required_when?: Record<string, unknown>;
    }>;
  };
  models: {
    kind: string;
    url?: string;
    values?: string[];
  };
}
