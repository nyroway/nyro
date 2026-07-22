package database

import (
	"io"
	"log"
	"time"

	"gorm.io/gorm/logger"
)

// gormSlowThreshold is how long a query may take before it is logged. Kept at
// GORM's own default — the point of this logger is to be quiet about normal
// operation, not to stop reporting queries that are actually slow.
const gormSlowThreshold = 200 * time.Millisecond

// newGormLogger builds the logger shared by every backend.
//
// GORM's defaults are aimed at interactive development and are wrong for a
// long-running service in four separate ways; each field below fixes one.
//
// GORM's default writes to stdout. Everything else in nyro logs to stderr, and
// stdout is what a caller would pipe — leaking SQL into it (and only SQL, since
// the real logs go elsewhere) is the wrong split. The writer is a parameter so
// tests can assert on what actually gets written.
func newGormLogger(w io.Writer) logger.Interface {
	return logger.New(
		log.New(w, "", log.LstdFlags),
		logger.Config{
			SlowThreshold: gormSlowThreshold,
			LogLevel:      logger.Warn,

			// The storage layer treats a missing row as a normal outcome, not
			// an error: settings.Get returns ("", nil) for an unset key, and
			// unset keys are the expected state for most optional settings. So
			// GORM's default of tracing every miss as an error logged ~13 SQL
			// blocks on every single boot, describing nothing an operator can
			// act on. A message that appears every time and never means
			// anything is worse than no message: it is what teaches people to
			// scroll past the one that does matter.
			IgnoreRecordNotFoundError: true,

			// Log `?` placeholders instead of interpolated values. Without
			// this, any slow or failing statement touching upstreams or
			// api_keys writes its parameters verbatim — which for
			// model.Upstream.CredentialsJSON means an upstream provider's key,
			// and for model.APIKey.KeyPlaintext (under --raw-api-keys) means a
			// working inbound key. Reading the log file should not be a way to
			// obtain credentials.
			ParameterizedQueries: true,

			// Colour is escape-code noise once logs are a file or journald
			// rather than a terminal, and nyro is normally the latter.
			Colorful: false,
		},
	)
}
