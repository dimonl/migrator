package migrate

import "fmt"

type sqliteDialect struct{}

func (s *sqliteDialect) DriverName() string {
	return "sqlite3"
}

func (s *sqliteDialect) Placeholder(_ int) string {
	return "?"
}

func (s *sqliteDialect) TableExistsSQL(tableName string) string {
	return fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='%s')", tableName)
}

func (s *sqliteDialect) LockTableSQL(_ string) string {
	return "" // SQLite uses exclusive transactions, no explicit table lock needed
}

func (s *sqliteDialect) CreateMigrationsTableSQL(tableName string) string {
	return fmt.Sprintf(`CREATE TABLE %s (
    version TEXT NOT NULL PRIMARY KEY,
    up_script TEXT NOT NULL,
    down_script TEXT NOT NULL,
    error TEXT)`, tableName)
}

func (s *sqliteDialect) GetLastMigrationSQL(tableName string) string {
	return fmt.Sprintf("SELECT version FROM %s", tableName)
}
