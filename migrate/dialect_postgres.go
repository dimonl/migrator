package migrate

import "fmt"

type postgresDialect struct{}

func (p *postgresDialect) DriverName() string {
	return "postgres"
}

func (p *postgresDialect) Placeholder(idx int) string {
	return fmt.Sprintf("$%d", idx)
}

func (p *postgresDialect) TableExistsSQL(tableName string) string {
	return fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name::text = '%s')", tableName)
}

func (p *postgresDialect) LockTableSQL(tableName string) string {
	return fmt.Sprintf("LOCK TABLE %s IN SHARE ROW EXCLUSIVE MODE", tableName)
}

func (p *postgresDialect) CreateMigrationsTableSQL(tableName string) string {
	return fmt.Sprintf(`CREATE TABLE %s (
    version varchar not null primary key,
    up_script varchar not null,
    down_script varchar not null,
    error varchar)`, tableName)
}

func (p *postgresDialect) GetLastMigrationSQL(tableName string) string {
	return fmt.Sprintf("SELECT version FROM %s", tableName)
}
