package plugin

// PluginKernel is a read-only facade over the registered lifecycle hooks,
// powering the admin "loaded extensions" view. Ported from
// crates/nyro-core/src/plugin/mod.rs (PluginKernel::global).
type PluginKernel struct{}

// Hooks returns the registered PhaseHooks (snapshot of the global registry).
func (PluginKernel) Hooks() []PhaseHook { return append([]PhaseHook(nil), hooks...) }

// Count returns the number of registered hooks.
func (PluginKernel) Count() int { return len(hooks) }

// Kernel returns the PluginKernel facade.
func Kernel() PluginKernel { return PluginKernel{} }
