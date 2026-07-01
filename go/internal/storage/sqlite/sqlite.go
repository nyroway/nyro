// Package sqlite is the persistent storage backend for the Nyro gateway,
// backed by SQLite via GORM (pure-Go modernc driver through glebarez/sqlite —
// no CGO). AutoMigrate is OFF; the schema (schema.sql, embedded) is applied
// idempotently by Migrate. Ported conceptually from crates/nyro-core/src/db.
package sqlite

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	_ "embed"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/nyroway/nyro/go/internal/storage"
)

//go:embed schema.sql
var schemaSQL string

// Backend is the SQLite storage backend.
type Backend struct {
	db *gorm.DB
}

// New opens a SQLite database at path (":memory:" or "" for an in-memory DB).
// Applies WAL journal + busy_timeout + max-connections for concurrent safety
// (matching Rust init_pool).
func New(path string) (*Backend, error) {
	if path == "" {
		path = ":memory:"
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(5)
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=5000")
	return &Backend{db: db}, nil
}

// NewPostgres opens a PostgreSQL database at dsn and returns a Backend with
// the same GORM models + Storage interface.
func NewPostgres(dsn string) (*Backend, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return &Backend{db: db}, nil
}

// NewMySQL opens a MySQL database at dsn and returns a Backend with the same
// GORM models + Storage interface.
func NewMySQL(dsn string) (*Backend, error) {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return &Backend{db: db}, nil
}

// DB exposes the underlying gorm.DB (for advanced callers / tests).
func (b *Backend) DB() *gorm.DB { return b.db }

// Storage returns the backend as a storage.Storage.
func (b *Backend) Storage() storage.Storage { return b }

// storage.Storage composition
func (b *Backend) Providers() storage.ProviderStore               { return providerStore{b} }
func (b *Backend) Models() storage.ModelStore                     { return modelStore{b} }
func (b *Backend) ModelBackends() storage.ModelBackendStore       { return backendStore{b} }
func (b *Backend) Settings() storage.SettingsStore                { return settingsStore{b} }
func (b *Backend) APIKeys() storage.ApiKeyStore                   { return apiKeyStore{b} }
func (b *Backend) Auth() storage.AuthAccessStore { return authStore{b} }
func (b *Backend) Bootstrap() storage.Bootstrap  { return b }

// Bootstrap
func (b *Backend) Init() error { return nil }
func (b *Backend) Migrate() error {
	if err := b.migrateLegacyTables(); err != nil {
		return err
	}
	if err := execAll(b.db, schemaSQL); err != nil {
		return err
	}
	if err := b.migrateLegacyColumns(); err != nil {
		return err
	}
	// Phase 4: the request_logs table was removed (request logs now live in
	// the parquet observability store). Drop any leftover copy so an upgrading
	// DB does not carry dead data. Idempotent; errors are non-fatal.
	b.db.Exec("DROP TABLE IF EXISTS request_logs")
	return nil
}

// migrateLegacyTables renames pre-migration Rust table names to the final
// schema (routes→models, route_targets→model_backends, api_key_routes→
// api_key_models). If the target already exists, drops the old leftover table.
func (b *Backend) migrateLegacyTables() error {
	renames := []struct{ old, new string }{
		{"routes", "models"},
		{"route_targets", "model_backends"},
		{"api_key_routes", "api_key_models"},
	}
	for _, r := range renames {
		var oldExists int64
		b.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", r.old).Scan(&oldExists)
		if oldExists == 0 {
			continue
		}
		var newExists int64
		b.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", r.new).Scan(&newExists)
		if newExists > 0 {
			b.db.Exec("DROP TABLE IF EXISTS " + r.old)
			continue
		}
		if err := b.db.Exec("ALTER TABLE " + r.old + " RENAME TO " + r.new).Error; err != nil {
			return err
		}
	}
	return nil
}

// migrateLegacyColumns renames pre-migration column names within the (already
// renamed) tables. Uses ALTER TABLE RENAME COLUMN (SQLite 3.25+, supported by
// modernc).
func (b *Backend) migrateLegacyColumns() error {
	columnRenames := []struct{ table, oldCol, newCol string }{
		{"models", "strategy", "balance"},
		{"models", "access_control", "enable_auth"},
		{"model_backends", "route_id", "model_id"},
		{"api_key_models", "route_id", "model_id"},
		{"api_keys", "key", "token"},
		{"settings", "key", "name"},
		{"providers", "is_active", "is_enabled"},
	}
	for _, r := range columnRenames {
		var exists int64
		b.db.Raw("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", r.table, r.oldCol).Scan(&exists)
		if exists > 0 {
			b.db.Exec("ALTER TABLE " + r.table + " RENAME COLUMN " + r.oldCol + " TO " + r.newCol)
		}
	}
	return nil
}

func (b *Backend) Health() (storage.StorageHealth, error) {
	h := storage.StorageHealth{Backend: "sqlite"}
	sqlDB, err := b.db.DB()
	if err != nil {
		return h, err
	}
	if err := sqlDB.Ping(); err != nil {
		return h, nil
	}
	h.CanConnect = true
	h.SchemaCompatible = true
	h.Writable = true
	return h, nil
}

// execAll strips -- comment lines, then runs each ";"-separated statement
// (modernc only runs the first statement on a single Exec call; comments may
// contain ";" which would otherwise corrupt the split).
func execAll(db *gorm.DB, script string) error {
	var cleaned strings.Builder
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		cleaned.WriteString(line)
		cleaned.WriteByte('\n')
	}
	for _, stmt := range strings.Split(cleaned.String(), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// ── helpers ──

func newID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func newToken() string {
	var buf [24]byte
	_, _ = rand.Read(buf[:])
	return "nyro_" + hex.EncodeToString(buf[:])
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

func balanceOrDefault(b storage.ModelBalance) storage.ModelBalance {
	if b == "" {
		return storage.BalanceWeighted
	}
	return b
}

func isNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

func (b *Backend) loadTargets(modelID string) []storage.ModelBackend {
	var targets []storage.ModelBackend
	b.db.Where("model_id = ?", modelID).Order("priority asc, weight desc").Find(&targets)
	return targets
}

func (b *Backend) loadBindings(apiKeyID string) []string {
	var ams []storage.ApiKeyModel
	b.db.Where("api_key_id = ?", apiKeyID).Find(&ams)
	out := make([]string, 0, len(ams))
	for _, am := range ams {
		out = append(out, am.ModelID)
	}
	return out
}

func withBindings(k storage.ApiKey, ids []string) storage.ApiKeyWithBindings {
	return storage.ApiKeyWithBindings{ApiKey: k, ModelIDs: ids}
}

func applyProviderUpdate(p *storage.Provider, in storage.UpdateProvider) {
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Vendor != nil {
		p.Vendor = *in.Vendor
	}
	if in.Protocol != nil {
		p.Protocol = *in.Protocol
	}
	if in.BaseURL != nil {
		p.BaseURL = *in.BaseURL
	}
	if in.PresetKey != nil {
		p.PresetKey = *in.PresetKey
	}
	if in.Channel != nil {
		p.Channel = *in.Channel
	}
	if in.ModelsSource != nil {
		p.ModelsSource = *in.ModelsSource
	}
	if in.StaticModels != nil {
		p.StaticModels = *in.StaticModels
	}
	if in.APIKey != nil {
		p.APIKey = *in.APIKey
	}
	if in.AuthMode != nil {
		p.AuthMode = *in.AuthMode
	}
	if in.UseProxy != nil {
		p.UseProxy = *in.UseProxy
	}
	if in.IsEnabled != nil {
		p.IsEnabled = *in.IsEnabled
	}
}

// ── providerStore ──

type providerStore struct{ b *Backend }

func (s providerStore) List() ([]storage.Provider, error) {
	var out []storage.Provider
	s.b.db.Order("name").Find(&out)
	return out, nil
}

func (s providerStore) Get(id string) (*storage.Provider, error) {
	var p storage.Provider
	if err := s.b.db.First(&p, "id = ?", id).Error; isNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s providerStore) Create(in storage.CreateProvider) (storage.Provider, error) {
	authMode := in.AuthMode
	if authMode == "" {
		authMode = "apikey"
	}
	now := nowISO()
	p := storage.Provider{
		ID: newID(), Name: in.Name, Vendor: in.Vendor, Protocol: in.Protocol, BaseURL: in.BaseURL,
		PresetKey: in.PresetKey, Channel: in.Channel, ModelsSource: in.ModelsSource,
		StaticModels: in.StaticModels, APIKey: in.APIKey, AuthMode: authMode, UseProxy: in.UseProxy,
		IsEnabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.b.db.Create(&p).Error; err != nil {
		return storage.Provider{}, err
	}
	return p, nil
}

func (s providerStore) Update(id string, in storage.UpdateProvider) (storage.Provider, error) {
	var p storage.Provider
	if err := s.b.db.First(&p, "id = ?", id).Error; isNotFound(err) {
		return storage.Provider{}, storage.ErrNotFound
	} else if err != nil {
		return storage.Provider{}, err
	}
	applyProviderUpdate(&p, in)
	p.UpdatedAt = nowISO()
	if err := s.b.db.Save(&p).Error; err != nil {
		return storage.Provider{}, err
	}
	return p, nil
}

func (s providerStore) Delete(id string) error {
	return s.b.db.Delete(&storage.Provider{}, "id = ?", id).Error
}

func (s providerStore) ExistsByName(name, excludeID string) (bool, error) {
	var c int64
	s.b.db.Model(&storage.Provider{}).Where("name = ? AND id <> ?", name, excludeID).Count(&c)
	return c > 0, nil
}

func (s providerStore) RecordTestResult(providerID string, result storage.ProviderTestResult) error {
	return s.b.db.Model(&storage.Provider{}).Where("id = ?", providerID).
		Updates(map[string]any{"last_test_success": result.Success, "last_test_at": result.TestedAt}).Error
}

// ── modelStore ──

type modelStore struct{ b *Backend }

func (s modelStore) List() ([]storage.Model, error) {
	var out []storage.Model
	s.b.db.Order("name").Find(&out)
	for i := range out {
		out[i].Targets = s.b.loadTargets(out[i].ID)
	}
	return out, nil
}

func (s modelStore) get(cond string, arg any) (*storage.Model, error) {
	var m storage.Model
	if err := s.b.db.First(&m, cond, arg).Error; isNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	m.Targets = s.b.loadTargets(m.ID)
	return &m, nil
}

func (s modelStore) Get(id string) (*storage.Model, error)      { return s.get("id = ?", id) }
func (s modelStore) ByName(name string) (*storage.Model, error) { return s.get("name = ?", name) }

func (s modelStore) Create(in storage.CreateModel) (storage.Model, error) {
	now := nowISO()
	m := storage.Model{
		ID: newID(), Name: in.Name, Balance: balanceOrDefault(in.Balance),
		EnableAuth: in.EnableAuth, EnablePayload: in.EnablePayload, IsEnabled: true, CreatedAt: now,
	}
	if len(in.Targets) > 0 {
		m.TargetProvider = in.Targets[0].ProviderID
		m.TargetModel = in.Targets[0].Model
	}
	err := s.b.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&m).Error; err != nil {
			return err
		}
		for _, t := range in.Targets {
			be := storage.ModelBackend{ID: newID(), ModelID: m.ID, ProviderID: t.ProviderID, Model: t.Model, Weight: t.Weight, Priority: t.Priority, CreatedAt: now}
			if err := tx.Create(&be).Error; err != nil {
				return err
			}
			m.Targets = append(m.Targets, be)
		}
		return nil
	})
	return m, err
}

func (s modelStore) Update(id string, in storage.UpdateModel) (storage.Model, error) {
	var m storage.Model
	if err := s.b.db.First(&m, "id = ?", id).Error; isNotFound(err) {
		return storage.Model{}, storage.ErrNotFound
	} else if err != nil {
		return storage.Model{}, err
	}
	if in.Name != nil {
		m.Name = *in.Name
	}
	if in.Balance != nil {
		m.Balance = *in.Balance
	}
	if in.EnableAuth != nil {
		m.EnableAuth = *in.EnableAuth
	}
	if in.EnablePayload != nil {
		m.EnablePayload = in.EnablePayload
	}
	if in.IsEnabled != nil {
		m.IsEnabled = *in.IsEnabled
	}
	err := s.b.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&m).Error; err != nil {
			return err
		}
		if in.Targets != nil {
			if err := tx.Where("model_id = ?", id).Delete(&storage.ModelBackend{}).Error; err != nil {
				return err
			}
			now := nowISO()
			for _, t := range *in.Targets {
				be := storage.ModelBackend{ID: newID(), ModelID: id, ProviderID: t.ProviderID, Model: t.Model, Weight: t.Weight, Priority: t.Priority, CreatedAt: now}
				if err := tx.Create(&be).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return storage.Model{}, err
	}
	m.Targets = s.b.loadTargets(id)
	return m, nil
}

func (s modelStore) Delete(id string) error {
	return s.b.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("model_id = ?", id).Delete(&storage.ModelBackend{}).Error; err != nil {
			return err
		}
		return tx.Delete(&storage.Model{}, "id = ?", id).Error
	})
}

func (s modelStore) ExistsByName(name, excludeID string) (bool, error) {
	var c int64
	s.b.db.Model(&storage.Model{}).Where("name = ? AND id <> ?", name, excludeID).Count(&c)
	return c > 0, nil
}

// ── backendStore ──

type backendStore struct{ b *Backend }

func (s backendStore) ListByModel(modelID string) ([]storage.ModelBackend, error) {
	var out []storage.ModelBackend
	s.b.db.Where("model_id = ?", modelID).Order("priority asc, weight desc").Find(&out)
	return out, nil
}

func (s backendStore) SetBackends(modelID string, targets []storage.CreateModelBackend) ([]storage.ModelBackend, error) {
	now := nowISO()
	var out []storage.ModelBackend
	err := s.b.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("model_id = ?", modelID).Delete(&storage.ModelBackend{}).Error; err != nil {
			return err
		}
		for _, t := range targets {
			be := storage.ModelBackend{ID: newID(), ModelID: modelID, ProviderID: t.ProviderID, Model: t.Model, Weight: t.Weight, Priority: t.Priority, CreatedAt: now}
			if err := tx.Create(&be).Error; err != nil {
				return err
			}
			out = append(out, be)
		}
		return nil
	})
	return out, err
}

func (s backendStore) DeleteByModel(modelID string) error {
	return s.b.db.Where("model_id = ?", modelID).Delete(&storage.ModelBackend{}).Error
}

// ── settingsStore ──

type settingsStore struct{ b *Backend }

func (s settingsStore) Get(key string) (string, error) {
	var set storage.Setting
	if err := s.b.db.First(&set, "name = ?", key).Error; isNotFound(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return set.Value, nil
}

func (s settingsStore) Set(key, value string) error {
	return s.b.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&storage.Setting{Key: key, Value: value, UpdatedAt: nowISO()}).Error
}

func (s settingsStore) ListAll() ([]storage.Setting, error) {
	var out []storage.Setting
	s.b.db.Order("key").Find(&out)
	return out, nil
}

// ── apiKeyStore ──

type apiKeyStore struct{ b *Backend }

func (s apiKeyStore) List() ([]storage.ApiKeyWithBindings, error) {
	var keys []storage.ApiKey
	s.b.db.Order("name").Find(&keys)
	out := make([]storage.ApiKeyWithBindings, 0, len(keys))
	for _, k := range keys {
		out = append(out, withBindings(k, s.b.loadBindings(k.ID)))
	}
	return out, nil
}

func (s apiKeyStore) Get(id string) (*storage.ApiKeyWithBindings, error) {
	var k storage.ApiKey
	if err := s.b.db.First(&k, "id = ?", id).Error; isNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	wb := withBindings(k, s.b.loadBindings(k.ID))
	return &wb, nil
}

func (s apiKeyStore) Create(in storage.CreateApiKey) (storage.ApiKeyWithBindings, error) {
	now := nowISO()
	token := in.Token
	if token == "" {
		token = newToken()
	}
	k := storage.ApiKey{
		ID: newID(), Token: token, Name: in.Name,
		RPM: in.RPM, RPD: in.RPD, TPM: in.TPM, TPD: in.TPD,
		IsEnabled: true, ExpiresAt: in.ExpiresAt, CreatedAt: now, UpdatedAt: now,
	}
	err := s.b.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&k).Error; err != nil {
			return err
		}
		for _, mid := range in.ModelIDs {
			if err := tx.Create(&storage.ApiKeyModel{APIKeyID: k.ID, ModelID: mid}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return storage.ApiKeyWithBindings{}, err
	}
	return withBindings(k, s.b.loadBindings(k.ID)), nil
}

func (s apiKeyStore) Update(id string, in storage.UpdateApiKey) (storage.ApiKeyWithBindings, error) {
	var k storage.ApiKey
	if err := s.b.db.First(&k, "id = ?", id).Error; isNotFound(err) {
		return storage.ApiKeyWithBindings{}, storage.ErrNotFound
	} else if err != nil {
		return storage.ApiKeyWithBindings{}, err
	}
	if in.Name != nil {
		k.Name = *in.Name
	}
	if in.RPM != nil {
		k.RPM = in.RPM
	}
	if in.RPD != nil {
		k.RPD = in.RPD
	}
	if in.TPM != nil {
		k.TPM = in.TPM
	}
	if in.TPD != nil {
		k.TPD = in.TPD
	}
	if in.IsEnabled != nil {
		k.IsEnabled = *in.IsEnabled
	}
	if in.ExpiresAt != nil {
		k.ExpiresAt = *in.ExpiresAt
	}
	k.UpdatedAt = nowISO()
	err := s.b.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&k).Error; err != nil {
			return err
		}
		if in.ModelIDs != nil {
			if err := tx.Where("api_key_id = ?", id).Delete(&storage.ApiKeyModel{}).Error; err != nil {
				return err
			}
			for _, mid := range *in.ModelIDs {
				if err := tx.Create(&storage.ApiKeyModel{APIKeyID: id, ModelID: mid}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return storage.ApiKeyWithBindings{}, err
	}
	return withBindings(k, s.b.loadBindings(id)), nil
}

func (s apiKeyStore) Delete(id string) error {
	return s.b.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("api_key_id = ?", id).Delete(&storage.ApiKeyModel{}).Error; err != nil {
			return err
		}
		return tx.Delete(&storage.ApiKey{}, "id = ?", id).Error
	})
}

func (s apiKeyStore) ExistsByName(name, excludeID string) (bool, error) {
	var c int64
	s.b.db.Model(&storage.ApiKey{}).Where("name = ? AND id <> ?", name, excludeID).Count(&c)
	return c > 0, nil
}

// ── authStore ──

type authStore struct{ b *Backend }

func (s authStore) FindAPIKey(rawKey string) (*storage.ApiKeyAccessRecord, error) {
	var k storage.ApiKey
	if err := s.b.db.First(&k, "token = ?", rawKey).Error; isNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &storage.ApiKeyAccessRecord{
		ID: k.ID, Name: k.Name, IsEnabled: k.IsEnabled, ExpiresAt: k.ExpiresAt,
		RPM: k.RPM, RPD: k.RPD, TPM: k.TPM, TPD: k.TPD,
	}, nil
}

func (s authStore) ModelBindingExists(apiKeyID, modelID string) (bool, error) {
	var c int64
	s.b.db.Model(&storage.ApiKeyModel{}).Where("api_key_id = ? AND model_id = ?", apiKeyID, modelID).Count(&c)
	return c > 0, nil
}

func (s authStore) ListBoundModelIDs(apiKeyID string) ([]string, error) {
	return s.b.loadBindings(apiKeyID), nil
}

