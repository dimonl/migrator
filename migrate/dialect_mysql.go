package migrate

import "fmt"

type mysqlDialect struct{}

func (m *mysqlDialect) DriverName() string {
	return "mysql"
}

func (m *mysqlDialect) Placeholder(_ int) string {
	return "?"
}

func (m *mysqlDialect) TableExistsSQL(tableName string) string {
	return fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = '%s')", tableName)
}

func (m *mysqlDialect) LockTableSQL(tableName string) string {
	return fmt.Sprintf("LOCK TABLES %s WRITE", tableName)
}

func (m *mysqlDialect) CreateMigrationsTableSQL(tableName string) string {
	return fmt.Sprintf(`CREATE TABLE %s (
    version VARCHAR(255) NOT NULL PRIMARY KEY,
    up_script TEXT NOT NULL,
    down_script TEXT NOT NULL,
    error TEXT)`, tableName)
}

func (m *mysqlDialect) GetLastMigrationSQL(tableName string) string {
	return fmt.Sprintf("SELECT version FROM %s", tableName)
}
