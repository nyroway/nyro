-- Create "consumer_keys" table
CREATE TABLE "public"."consumer_keys" (
  "id" text NOT NULL,
  "consumer_id" character varying(191) NOT NULL,
  "name" character varying(255) NOT NULL,
  "key_preview" character varying(191) NOT NULL,
  "key_hash" text NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  "expires_at" text NULL,
  "last_used_at" text NULL,
  "created_at" text NOT NULL,
  "updated_at" text NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_consumer_key_name" to table: "consumer_keys"
CREATE UNIQUE INDEX "idx_consumer_key_name" ON "public"."consumer_keys" ("consumer_id", "name");
-- Create index "idx_consumer_keys_key_preview" to table: "consumer_keys"
CREATE INDEX "idx_consumer_keys_key_preview" ON "public"."consumer_keys" ("key_preview");
-- Create "consumer_quotas" table
CREATE TABLE "public"."consumer_quotas" (
  "id" text NOT NULL,
  "consumer_id" text NOT NULL,
  "quota_type" text NOT NULL,
  "quota_limit" bigint NOT NULL,
  "window" text NULL,
  "currency" text NULL,
  "created_at" text NOT NULL,
  "updated_at" text NOT NULL,
  PRIMARY KEY ("id")
);
-- Create "consumer_routes" table
CREATE TABLE "public"."consumer_routes" (
  "consumer_id" text NOT NULL,
  "route_id" text NOT NULL,
  PRIMARY KEY ("consumer_id", "route_id")
);
-- Create "consumers" table
CREATE TABLE "public"."consumers" (
  "id" text NOT NULL,
  "name" character varying(255) NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  "metadata_json" text NULL,
  "protocols_json" text NULL,
  "ip_allowlist_json" text NULL,
  "max_input_tokens" bigint NULL,
  "max_output_tokens" bigint NULL,
  "max_request_body_bytes" bigint NULL,
  "created_at" text NOT NULL,
  "updated_at" text NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_consumers_name" to table: "consumers"
CREATE UNIQUE INDEX "idx_consumers_name" ON "public"."consumers" ("name");
-- Create "route_upstreams" table
CREATE TABLE "public"."route_upstreams" (
  "id" text NOT NULL,
  "route_id" character varying(191) NOT NULL,
  "upstream_id" character varying(191) NOT NULL,
  "model" character varying(255) NOT NULL,
  "weight" integer NOT NULL DEFAULT 100,
  "priority" integer NOT NULL DEFAULT 1,
  "enabled" boolean NOT NULL DEFAULT true,
  "created_at" text NOT NULL,
  "updated_at" text NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_route_upstream_model" to table: "route_upstreams"
CREATE UNIQUE INDEX "idx_route_upstream_model" ON "public"."route_upstreams" ("route_id", "upstream_id", "model");
-- Create "routes" table
CREATE TABLE "public"."routes" (
  "id" text NOT NULL,
  "model" character varying(255) NOT NULL,
  "balance" text NOT NULL DEFAULT 'weighted',
  "enable_auth" boolean NOT NULL DEFAULT false,
  "enable_payload" boolean NOT NULL DEFAULT false,
  "enabled" boolean NOT NULL DEFAULT true,
  "created_at" text NOT NULL,
  "updated_at" text NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_routes_model" to table: "routes"
CREATE UNIQUE INDEX "idx_routes_model" ON "public"."routes" ("model");
-- Create "settings" table
CREATE TABLE "public"."settings" (
  "key" text NOT NULL,
  "value" text NOT NULL,
  "updated_at" text NOT NULL,
  PRIMARY KEY ("key")
);
-- Create "upstreams" table
CREATE TABLE "public"."upstreams" (
  "id" text NOT NULL,
  "name" character varying(255) NOT NULL,
  "provider" text NOT NULL DEFAULT 'custom',
  "protocol" text NULL,
  "base_url" text NULL,
  "credentials_json" text NULL,
  "models_json" text NULL,
  "models_url" text NULL,
  "proxy_url" text NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  "created_at" text NOT NULL,
  "updated_at" text NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_upstreams_name" to table: "upstreams"
CREATE UNIQUE INDEX "idx_upstreams_name" ON "public"."upstreams" ("name");
