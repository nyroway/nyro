package provider

type Definition struct {
	ID              string
	Name            string
	DefaultProtocol string
	DefaultModel    string
	Protocols       []Protocol
	Credentials     CredentialSchema
	ModelsURL       string
	Extra           map[string]any
	Priority        int
}

type Protocol struct {
	ID      string
	BaseURL string
}

type CredentialSchema struct {
	Fields []CredentialField
}

type CredentialField struct {
	Name         string
	Type         string
	Required     bool
	Default      string
	Values       []string
	Env          string
	RequiredWhen map[string]any
}

func SupportsProtocol(definition Definition, protocolID string) bool {
	for _, supported := range definition.Protocols {
		if supported.ID == protocolID {
			return true
		}
	}
	return false
}

func HealthCheckModel(definition Definition) string {
	return definition.DefaultModel
}
