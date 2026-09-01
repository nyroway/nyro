package llm

// Workload identifies a model interaction contract understood by the LLM
// runtime. Only workloads implemented by Nyro belong here.
type Workload string

const (
	WorkloadChat      Workload = "chat"
	WorkloadEmbedding Workload = "embedding"
)

// ModelRequest is the narrow request contract shared by routing, quota, and
// other workload-neutral LLM pipeline stages.
type ModelRequest interface {
	Workload() Workload
	ModelID() string
	SetModelID(string)
}
