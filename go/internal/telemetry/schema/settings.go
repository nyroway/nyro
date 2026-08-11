package schema

var settingSignalNames = map[Signal]string{
	SignalLogs:    "logs",
	SignalMetrics: "metrics",
	SignalTraces:  "traces",
}

// IsExporterSettingKey reports whether key configures a gateway-side
// telemetry exporter. Retention settings are deliberately excluded because
// they are consumed only by the embedded observe store.
func IsExporterSettingKey(key string) bool {
	for _, signal := range []Signal{SignalLogs, SignalMetrics, SignalTraces} {
		name := settingSignalNames[signal]
		if key == "obs_"+name+"_exporter" {
			return true
		}
		for _, def := range ExportersFor(signal) {
			for _, field := range def.Fields {
				if key == "obs_"+name+"_"+string(def.Kind)+"_"+field.Name {
					return true
				}
			}
		}
	}
	return false
}
