package database

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/model"
)

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

func newID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func isNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

// rawJSON converts a DB-stored JSON string column to json.RawMessage (empty
// string means "not set").
func rawJSON(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

// jsonRaw converts a json.RawMessage DTO field to the DB-stored string column.
func jsonRaw(rm json.RawMessage) string {
	if len(rm) == 0 {
		return ""
	}
	return string(rm)
}

func upstreamFromModel(m *model.Upstream) storage.Upstream {
	return storage.Upstream{
		ID:              m.ID,
		Name:            m.Name,
		Protocol:        m.Protocol,
		BaseURL:         m.BaseURL,
		CredentialsJSON: rawJSON(m.CredentialsJSON),
		ModelsJSON:      rawJSON(m.ModelsJSON),
		ProxyURL:        m.ProxyURL,
		Enabled:         m.Enabled,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

func routeFromModel(m *model.Route) storage.Route {
	enablePayload := m.EnablePayload
	return storage.Route{
		ID:            m.ID,
		Model:         m.Model,
		Balance:       storage.ModelBalance(m.Balance),
		EnableAuth:    m.EnableAuth,
		EnablePayload: &enablePayload,
		Enabled:       m.Enabled,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

func routeUpstreamFromModel(m *model.RouteUpstream) storage.RouteUpstream {
	return storage.RouteUpstream{
		ID:         m.ID,
		RouteID:    m.RouteID,
		UpstreamID: m.UpstreamID,
		Model:      m.Model,
		Weight:     m.Weight,
		Priority:   m.Priority,
		Enabled:    m.Enabled,
		CreatedAt:  m.CreatedAt,
	}
}

func consumerFromModel(m *model.Consumer) storage.Consumer {
	return storage.Consumer{
		ID:        m.ID,
		Name:      m.Name,
		Enabled:   m.Enabled,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func consumerKeyFromModel(m *model.ConsumerKey) storage.ConsumerKey {
	return storage.ConsumerKey{
		ID:         m.ID,
		ConsumerID: m.ConsumerID,
		Name:       m.Name,
		KeyPrefix:  m.KeyPrefix,
		KeyHash:    m.KeyHash,
		Enabled:    m.Enabled,
		ExpiresAt:  m.ExpiresAt,
		LastUsedAt: m.LastUsedAt,
		CreatedAt:  m.CreatedAt,
		UpdatedAt:  m.UpdatedAt,
	}
}

func consumerQuotaFromModel(m *model.ConsumerQuota) storage.ConsumerQuota {
	return storage.ConsumerQuota{
		ID:         m.ID,
		ConsumerID: m.ConsumerID,
		QuotaType:  m.QuotaType,
		QuotaLimit: m.QuotaLimit,
		Window:     m.Window,
	}
}
