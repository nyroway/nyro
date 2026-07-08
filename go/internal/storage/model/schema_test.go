package model

import (
	"testing"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAutoMigrateCreatesConfigSchemaTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(All()...); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}

	for _, table := range []string{
		"upstreams",
		"routes",
		"route_upstreams",
		"consumers",
		"consumer_keys",
		"consumer_routes",
		"consumer_quotas",
		"settings",
	} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("missing table %s", table)
		}
	}

	for table, columns := range map[string][]string{
		"upstreams":       {"provider", "credentials_json", "models_json", "models_url", "proxy_url"},
		"routes":          {"model", "balance", "enable_auth", "enable_payload"},
		"route_upstreams": {"route_id", "upstream_id", "model", "weight", "priority"},
		"consumer_keys":   {"consumer_id", "key_preview", "key_hash", "last_used_at"},
		"consumer_quotas": {"consumer_id", "quota_type", "quota_limit", "window"},
		"settings":        {"key", "value", "updated_at"},
	} {
		for _, column := range columns {
			if !db.Migrator().HasColumn(table, column) {
				t.Fatalf("missing column %s.%s", table, column)
			}
		}
	}
	if db.Migrator().HasColumn("upstreams", "test_model") {
		t.Fatal("upstreams.test_model should not exist; health-check defaults belong to provider definitions")
	}
}
