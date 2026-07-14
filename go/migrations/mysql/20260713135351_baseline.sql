-- Create "consumer_keys" table
CREATE TABLE `consumer_keys` (
  `id` varchar(191) NOT NULL,
  `consumer_id` varchar(191) NOT NULL,
  `name` varchar(255) NOT NULL,
  `key_preview` varchar(191) NOT NULL,
  `key_hash` longtext NOT NULL,
  `enabled` bool NOT NULL DEFAULT 1,
  `expires_at` longtext NULL,
  `last_used_at` longtext NULL,
  `created_at` longtext NOT NULL,
  `updated_at` longtext NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE INDEX `idx_consumer_key_name` (`consumer_id`, `name`),
  INDEX `idx_consumer_keys_key_preview` (`key_preview`)
) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
-- Create "consumer_quotas" table
CREATE TABLE `consumer_quotas` (
  `id` varchar(191) NOT NULL,
  `consumer_id` longtext NOT NULL,
  `quota_type` longtext NOT NULL,
  `quota_limit` bigint NOT NULL,
  `window` longtext NULL,
  `currency` longtext NULL,
  `created_at` longtext NOT NULL,
  `updated_at` longtext NOT NULL,
  PRIMARY KEY (`id`)
) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
-- Create "consumer_routes" table
CREATE TABLE `consumer_routes` (
  `consumer_id` varchar(191) NOT NULL,
  `route_id` varchar(191) NOT NULL,
  PRIMARY KEY (`consumer_id`, `route_id`)
) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
-- Create "consumers" table
CREATE TABLE `consumers` (
  `id` varchar(191) NOT NULL,
  `name` varchar(255) NOT NULL,
  `enabled` bool NOT NULL DEFAULT 1,
  `metadata_json` longtext NULL,
  `protocols_json` longtext NULL,
  `ip_allowlist_json` longtext NULL,
  `max_input_tokens` bigint NULL,
  `max_output_tokens` bigint NULL,
  `max_request_body_bytes` bigint NULL,
  `created_at` longtext NOT NULL,
  `updated_at` longtext NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE INDEX `idx_consumers_name` (`name`)
) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
-- Create "route_upstreams" table
CREATE TABLE `route_upstreams` (
  `id` varchar(191) NOT NULL,
  `route_id` varchar(191) NOT NULL,
  `upstream_id` varchar(191) NOT NULL,
  `model` varchar(255) NOT NULL,
  `weight` int NOT NULL DEFAULT 100,
  `priority` int NOT NULL DEFAULT 1,
  `enabled` bool NOT NULL DEFAULT 1,
  `created_at` longtext NOT NULL,
  `updated_at` longtext NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE INDEX `idx_route_upstream_model` (`route_id`, `upstream_id`, `model`)
) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
-- Create "routes" table
CREATE TABLE `routes` (
  `id` varchar(191) NOT NULL,
  `model` varchar(255) NOT NULL,
  `balance` varchar(191) NOT NULL DEFAULT "weighted",
  `enable_auth` bool NOT NULL DEFAULT 0,
  `enable_payload` bool NOT NULL DEFAULT 0,
  `enabled` bool NOT NULL DEFAULT 1,
  `created_at` longtext NOT NULL,
  `updated_at` longtext NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE INDEX `idx_routes_model` (`model`)
) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
-- Create "settings" table
CREATE TABLE `settings` (
  `key` varchar(191) NOT NULL,
  `value` longtext NOT NULL,
  `updated_at` longtext NOT NULL,
  PRIMARY KEY (`key`)
) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
-- Create "upstreams" table
CREATE TABLE `upstreams` (
  `id` varchar(191) NOT NULL,
  `name` varchar(255) NOT NULL,
  `provider` varchar(191) NOT NULL DEFAULT "custom",
  `protocol` longtext NULL,
  `base_url` longtext NULL,
  `credentials_json` longtext NULL,
  `models_json` longtext NULL,
  `models_url` longtext NULL,
  `proxy_url` longtext NULL,
  `enabled` bool NOT NULL DEFAULT 1,
  `created_at` longtext NOT NULL,
  `updated_at` longtext NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE INDEX `idx_upstreams_name` (`name`)
) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
