package migrate

import "fmt"

// Dialect encapsulates database-specific SQL syntax and behavior.
type Dialect interface {
	// DriverName returns the database driver name (e.g. "postgres", "mysql", "sqlite3").
	DriverName() string

	// Placeholder returns the parameter placeholder for the given index (1-based).
	// PostgreSQL: $1, MySQL/SQLite: "?".
	Placeholder(idx int) string

	// TableExistsSQL returns a query that returns true if the given table exists.
	TableExistsSQL(tableName string) string

	// LockTableSQL returns a statement to lock the migrations table, or empty string if locking is not needed.
	LockTableSQL(tableName string) string

	// CreateMigrationsTableSQL returns the CREATE TABLE statement for the migrations table.
	CreateMigrationsTableSQL(tableName string) string

	// GetLastMigrationSQL returns a query to get the last applied migration version
	// from the legacy schema_migrations table.
	GetLastMigrationSQL(tableName string) string
}

// knownDialects maps driver names to dialect factories.
var knownDialects = map[string]Dialect{
	"postgres": &postgresDialect{},
	"pgx":      &postgresDialect{},
	"mysql":    &mysqlDialect{},
	"sqlite3":  &sqliteDialect{},
	"sqlite":   &sqliteDialect{},
}

// DetectDialect returns a Dialect for the given driver name.
// It returns an error if the driver is not supported.
func DetectDialect(driverName string) (Dialect, error) {
	d, ok := knownDialects[driverName]
	if !ok {
		return nil, fmt.Errorf("unsupported database driver: %s", driverName)
	}
	return d, nil
}
