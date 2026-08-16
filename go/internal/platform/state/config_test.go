package state_test

import (
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/platform/state"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		want    state.Config
		wantErr string
	}{
		{name: "absent defaults to memory", want: state.Config{Kind: state.KindMemory}},
		{name: "explicit memory", values: map[string]string{state.SettingTypeKey: "memory"}, want: state.Config{Kind: state.KindMemory}},
		{name: "redis", values: map[string]string{state.SettingTypeKey: "redis", state.SettingURLKey: "redis://default:secret@redis.example:6379/2"}, want: state.Config{Kind: state.KindRedis, URL: "redis://default:secret@redis.example:6379/2"}},
		{name: "redis tls is unsupported", values: map[string]string{state.SettingTypeKey: "redis", state.SettingURLKey: "rediss://redis.example:6379/0"}, wantErr: "valid redis:// URL"},
		{name: "url without type", values: map[string]string{state.SettingURLKey: "redis://redis.example:6379"}, wantErr: "state.type is required"},
		{name: "memory with url", values: map[string]string{state.SettingTypeKey: "memory", state.SettingURLKey: "redis://redis.example:6379"}, wantErr: "state.url is not allowed"},
		{name: "redis without url", values: map[string]string{state.SettingTypeKey: "redis"}, wantErr: "state.url is required"},
		{name: "unknown type", values: map[string]string{state.SettingTypeKey: "etcd"}, wantErr: "unknown state type \"etcd\""},
		{name: "wrong url scheme", values: map[string]string{state.SettingTypeKey: "redis", state.SettingURLKey: "http://redis.example:6379"}, wantErr: "valid redis:// URL"},
		{name: "empty fragment", values: map[string]string{state.SettingTypeKey: "redis", state.SettingURLKey: "redis://redis.example:6379#"}, wantErr: "valid redis:// URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := state.LoadConfig(func(key string) (string, error) {
				return tt.values[key], nil
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("LoadConfig() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("LoadConfig() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRedactedURLHidesPasswordAndQuery(t *testing.T) {
	got := state.RedactedURL("redis://alice:secret@redis.example:6379/1?client_name=secret-token")
	if strings.Contains(got, "secret") || strings.Contains(got, "?") || got != "redis://alice:xxxxx@redis.example:6379/1" {
		t.Fatalf("RedactedURL() = %q", got)
	}
}

func TestRedactedURLRejectsInvalidInputWithoutLeakingIt(t *testing.T) {
	raw := "://secret"
	if got := state.RedactedURL(raw); got != "<invalid>" || strings.Contains(got, "secret") {
		t.Fatalf("RedactedURL() = %q", got)
	}
}

func TestIsSettingKey(t *testing.T) {
	if !state.IsSettingKey("state.type") || !state.IsSettingKey("state.url") || state.IsSettingKey("state.quota") {
		t.Fatal("unexpected State setting-key classification")
	}
}
