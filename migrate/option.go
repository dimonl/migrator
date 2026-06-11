package migrate

import (
	"log/slog"
)

// Option configures a Migrator.
type Option func(*Migrator)

// WithDisabledDownMigrations disables rollback migrations; use for production
// environments where down migrations should never run automatically.
func WithDisabledDownMigrations() Option {
	return func(mg *Migrator) {
		mg.disableDown = true
	}
}

// WithTableName sets a custom name for the migrations tracking table.
func WithTableName(name string) Option {
	return func(mg *Migrator) {
		mg.tableName = name
	}
}

// WithLegacyTableName sets a custom name for the legacy schema_migrations table.
func WithLegacyTableName(name string) Option {
	return func(mg *Migrator) {
		mg.legacyTableName = name
	}
}

// WithDialect sets a specific dialect for the migrator.
// If not provided, the dialect is auto-detected from the database driver.
func WithDialect(dialect Dialect) Option {
	return func(mg *Migrator) {
		mg.dialect = dialect
	}
}

// WithLogger sets a structured logger for the migrator.
func WithLogger(logger *slog.Logger) Option {
	return func(mg *Migrator) {
		mg.logger = logger
	}
}

// WithDryRun enables dry-run mode. When enabled, migrations are not executed;
// instead, the SQL that would be executed is collected and can be inspected.
func WithDryRun() Option {
	return func(mg *Migrator) {
		mg.dryRun = true
	}
}
