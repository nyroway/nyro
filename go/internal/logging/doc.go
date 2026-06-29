// Package logging implements the async request-log collector: a buffered
// channel sink that batches wire-level log entries (client+upstream
// headers/bodies, usage, stream metrics) to storage and applies the retention
// policy.
//
// Ported from crates/nyro-core/src/logging.
package logging
