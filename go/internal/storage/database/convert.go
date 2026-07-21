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
		Provider:        m.Provider,
		Protocol:        m.Protocol,
		BaseURL:         m.BaseURL,
		CredentialsJSON: rawJSON(m.CredentialsJSON),
		ModelsJSON:      rawJSON(m.ModelsJSON),
		ModelsURL:       m.ModelsURL,
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
	out := storage.Consumer{
		ID:          m.ID,
		Name:        m.Name,
		Enabled:     m.Enabled,
		Metadata:    stringMapFromJSON(m.MetadataJSON),
		Protocols:   stringSliceFromJSON(m.ProtocolsJSON),
		IPAllowlist: stringSliceFromJSON(m.IPAllowlistJSON),
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
	if m.MaxInputTokens != 0 || m.MaxOutputTokens != 0 || m.MaxRequestBodyBytes != 0 {
		out.Limits = &storage.ConsumerLimits{
			MaxInputTokens:      m.MaxInputTokens,
			MaxOutputTokens:     m.MaxOutputTokens,
			MaxRequestBodyBytes: m.MaxRequestBodyBytes,
		}
	}
	return out
}

// stringMapFromJSON decodes a DB-stored JSON object column into a
// map[string]string (empty string means "not set").
func stringMapFromJSON(s string) map[string]string {
	if s == "" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// stringMapToJSON encodes a map[string]string DTO field to the DB-stored JSON
// string column.
func stringMapToJSON(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// stringSliceFromJSON decodes a DB-stored JSON array column into a []string
// (empty string means "not set").
func stringSliceFromJSON(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// stringSliceToJSON encodes a []string DTO field to the DB-stored JSON string
// column.
func stringSliceToJSON(v []string) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func consumerKeyFromModel(m *model.ConsumerKey) storage.ConsumerKey {
	k := storage.ConsumerKey{
		ID:           m.ID,
		ConsumerID:   m.ConsumerID,
		Name:         m.Name,
		KeyPreview:   m.KeyPreview,
		KeyHash:      m.KeyHash,
		KeyPlaintext: m.KeyPlaintext,
		Enabled:      m.Enabled,
		ExpiresAt:    m.ExpiresAt,
		LastUsedAt:   m.LastUsedAt,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
	// For plaintext-stored keys, expose the recoverable raw key on read paths
	// via Token (create sets Token to the same value explicitly).
	if k.KeyPlaintext != "" {
		k.Token = k.KeyPlaintext
	}
	return k
}

func consumerQuotaFromModel(m *model.ConsumerQuota) storage.ConsumerQuota {
	return storage.ConsumerQuota{
		ID:         m.ID,
		ConsumerID: m.ConsumerID,
		QuotaType:  m.QuotaType,
		QuotaLimit: m.QuotaLimit,
		Window:     m.Window,
		Currency:   m.Currency,
	}
}
