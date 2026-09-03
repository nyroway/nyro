package runtime

import (
	"testing"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
)

func TestSettingsFromSnapshotDefaults(t *testing.T) {
	settings := SettingsFromSnapshot((&configsnapshot.Builder{}).Build())
	if settings.RequestTimeout != 120*time.Second {
		t.Errorf("RequestTimeout = %v, want 120s", settings.RequestTimeout)
	}
	if settings.ConnectTimeout != 30*time.Second {
		t.Errorf("ConnectTimeout = %v, want 30s", settings.ConnectTimeout)
	}
	if settings.MaxRetries != 2 {
		t.Errorf("MaxRetries = %d, want 2", settings.MaxRetries)
	}
	for _, code := range []int{429, 500, 502, 503, 504} {
		if !settings.RetryOnStatus[code] {
			t.Errorf("default RetryOnStatus missing %d", code)
		}
	}
}

func TestSettingsFromSnapshotOverrides(t *testing.T) {
	var builder configsnapshot.Builder
	builder.SetSetting("proxy.request_timeout", "45s")
	builder.SetSetting("proxy.connect_timeout", "5s")
	builder.SetSetting("proxy.max_retries", "4")
	builder.SetSetting("proxy.retry_on_status", `[408,429]`)

	settings := SettingsFromSnapshot(builder.Build())
	if settings.RequestTimeout != 45*time.Second {
		t.Errorf("RequestTimeout = %v, want 45s", settings.RequestTimeout)
	}
	if settings.ConnectTimeout != 5*time.Second {
		t.Errorf("ConnectTimeout = %v, want 5s", settings.ConnectTimeout)
	}
	if settings.MaxRetries != 4 {
		t.Errorf("MaxRetries = %d, want 4", settings.MaxRetries)
	}
	if !settings.RetryOnStatus[408] || !settings.RetryOnStatus[429] || settings.RetryOnStatus[500] {
		t.Errorf("RetryOnStatus = %v, want exactly {408,429}", settings.RetryOnStatus)
	}
}
