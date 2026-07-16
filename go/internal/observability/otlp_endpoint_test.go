package observability

import "testing"

func TestOtlpSignalURL(t *testing.T) {
	cases := []struct {
		name, endpoint, defaultPath, want string
	}{
		{"base url no path (the seed case)", "http://127.0.0.1:19531", otlpLogsPath, "http://127.0.0.1:19531/v1/logs"},
		{"base url trailing slash", "http://127.0.0.1:19531/", otlpMetricsPath, "http://127.0.0.1:19531/v1/metrics"},
		{"https base", "https://collector.example.com:4318", otlpTracesPath, "https://collector.example.com:4318/v1/traces"},
		{"explicit path respected", "http://host:4318/custom/logs", otlpLogsPath, "http://host:4318/custom/logs"},
		{"scheme-less left untouched", "127.0.0.1:19531", otlpLogsPath, "127.0.0.1:19531"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := otlpSignalURL(c.endpoint, c.defaultPath); got != c.want {
				t.Errorf("otlpSignalURL(%q, %q) = %q; want %q", c.endpoint, c.defaultPath, got, c.want)
			}
		})
	}
}
