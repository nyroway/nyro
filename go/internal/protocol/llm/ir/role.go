package ir

// Role is the conversational role of a Message.
// Ported from Role (serde rename_all = "lowercase").
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)
