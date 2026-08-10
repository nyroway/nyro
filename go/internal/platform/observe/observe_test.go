package observe_test

import (
	"errors"
	"testing"

	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/nyroway/nyro/go/internal/platform/observe"
)

func TestExportRequestRequiresExactlyOneSignal(t *testing.T) {
	tests := []observe.ExportRequest{
		{},
		{Logs: &collectlogs.ExportLogsServiceRequest{}, Traces: &collecttrace.ExportTraceServiceRequest{}},
	}
	for _, request := range tests {
		if _, err := request.Signal(); !errors.Is(err, observe.ErrInvalidExport) {
			t.Fatalf("Signal() error = %v, want ErrInvalidExport", err)
		}
	}
}
