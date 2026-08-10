package admin

import "sync"

type RuntimeServiceID string

const (
	ServiceControlPlane  RuntimeServiceID = "control-plane"
	ServiceEmbeddedProxy RuntimeServiceID = "embedded-proxy"
	ServiceRedisState    RuntimeServiceID = "redis-state"
	ServiceOTLPReceiver  RuntimeServiceID = "otlp-receiver"
)

type RuntimeServiceStatus string

const (
	ServiceRunning  RuntimeServiceStatus = "running"
	ServiceDisabled RuntimeServiceStatus = "disabled"
)

// RuntimeService is the non-secret runtime snapshot shown by the WebUI. It
// deliberately omits DSNs, passwords, tokens, and other credentials.
type RuntimeService struct {
	ID             RuntimeServiceID     `json:"id"`
	Status         RuntimeServiceStatus `json:"status"`
	Listen         string               `json:"listen,omitempty"`
	StorageBackend string               `json:"storage_backend,omitempty"`
	DataPath       string               `json:"data_path,omitempty"`
}

var runtimeServicesState struct {
	sync.RWMutex
	services []RuntimeService
}

// SetRuntimeServices installs the immutable serve-process snapshot exposed by
// GET /api/v1/runtime/services. Passing nil clears it for tests or standalone
// admin routers that have no process-level runtime metadata.
func SetRuntimeServices(services []RuntimeService) {
	runtimeServicesState.Lock()
	runtimeServicesState.services = append([]RuntimeService(nil), services...)
	runtimeServicesState.Unlock()
}

func runtimeServices() []RuntimeService {
	runtimeServicesState.RLock()
	defer runtimeServicesState.RUnlock()
	services := make([]RuntimeService, len(runtimeServicesState.services))
	copy(services, runtimeServicesState.services)
	return services
}
