package migrate

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
)

type migration struct {
	Version        int    `db:"version"`
	ContentUp      string `db:"up_script"`
	ContentDown    string `db:"down_script"`
	MigrationError string `db:"error"`
}

type Migrator struct {
	db              *sqlx.DB
	dialect         Dialect
	fs              fs.FS
	tableName       string
	legacyTableName string
	disableDown     bool
}

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

func NewMigrator(db *sqlx.DB, migrFS fs.FS, opts ...Option) *Migrator {
	mg := &Migrator{
		db:              db,
		fs:              migrFS,
		tableName:       "migrations",
		legacyTableName: "schema_migrations",
	}
	for _, opt := range opts {
		opt(mg)
	}

	// Auto-detect dialect if not set via option
	if mg.dialect == nil {
		driverName := db.DriverName()
		d, err := DetectDialect(driverName)
		if err != nil {
			// Store the error to be returned on first use
			// For now, just leave dialect nil — Migrate will check
			_ = err
		} else {
			mg.dialect = d
		}
	}

	return mg
}

func InitDB(ctx context.Context, dsn string, migrFS fs.FS, opts ...Option) (*sqlx.DB, error) {
	// Create a temporary migrator just to get the dialect
	temp := &Migrator{
		tableName:       "migrations",
		legacyTableName: "schema_migrations",
	}
	for _, opt := range opts {
		opt(temp)
	}

	driverName := "postgres"
	if temp.dialect != nil {
		driverName = temp.dialect.DriverName()
	}

	db, err := sqlx.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}

	if err = db.Ping(); err != nil {
		return nil, err
	}

	migrator := NewMigrator(db, migrFS, opts...)
	if migrator.dialect == nil {
		return nil, fmt.Errorf("failed to detect database dialect for driver: %s", db.DriverName())
	}

	if err = migrator.UpdateMigrationList(ctx); err != nil {
		return nil, fmt.Errorf("migrationList: %w", err)
	}
	if err = migrator.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}

	return db, nil
}

func (mg *Migrator) Migrate(ctx context.Context) (err error) {
	if mg.dialect == nil {
		return errors.New("migrator: dialect is not set")
	}

	tx, err := mg.lockMigrationTable(ctx)
	if err != nil {
		return fmt.Errorf("lock migration table: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
			return
		}
		err = tx.Commit()
	}()

	migrationsFS, err := mg.getMigrationsFromFiles()
	if err != nil {
		return err
	}

	migrationsDB, err := mg.getMigrationsFromDB(ctx, tx)
	if err != nil {
		return err
	}

	diffBD, diffFS := getDiff(migrationsDB, migrationsFS)

	if diffBD != nil && !mg.disableDown {
		sort.Slice(diffBD, func(left, right int) bool { return diffBD[left].Version > diffBD[right].Version })
		for i := range diffBD {
			err = execMigration(ctx, tx, diffBD[i].ContentDown)
			if err != nil {
				diffBD[i].MigrationError = err.Error()
				if mErr := mg.markMigrationAsDirtyToDB(ctx, tx, diffBD[i]); mErr != nil {
					return mErr
				}
				return err
			}

			err = mg.deleteMigrationFromDB(ctx, tx, diffBD[i])
			if err != nil {
				return err
			}
		}
	}

	for _, m := range diffFS {
		err = execMigration(ctx, tx, m.ContentUp)
		if err != nil {
			return err
		}
		err = mg.insertMigrationToDB(ctx, tx, m)
		if err != nil {
			return err
		}
	}

	return nil
}

func (mg *Migrator) getMigrationsFromFiles() ([]migration, error) {
	upMigrations, err := fs.Glob(mg.fs, "*.up.sql")
	if err != nil {
		return nil, fmt.Errorf("failed to get up migrations: %w", err)
	}

	downMigrations, err := fs.Glob(mg.fs, "*.down.sql")
	if err != nil {
		return nil, fmt.Errorf("failed to get down migrations: %w", err)
	}

	if len(upMigrations) != len(downMigrations) {
		return nil, errors.New("quantity of up migrations doesn't fit to quantity of down migrations")
	}

	migrations := make([]migration, 0, len(upMigrations))
	for _, entryUp := range upMigrations {
		if i := strings.Index(entryUp, "_"); i > -1 {
			n, err := strconv.Atoi(entryUp[:i])
			if err != nil {
				return nil, fmt.Errorf("failed to parse migration Version: %w", err)
			}
			b, err := fs.ReadFile(mg.fs, entryUp)
			if err != nil {
				return nil, fmt.Errorf("failed to read up migration: %w", err)
			}
			c, err := fs.ReadFile(mg.fs, strings.ReplaceAll(entryUp, ".up.", ".down."))
			if err != nil {
				return nil, fmt.Errorf("failed to read down migration: %w", err)
			}

			migrations = append(migrations, migration{
				Version:     n,
				ContentUp:   string(b),
				ContentDown: string(c),
			})
		}
	}
	slices.SortFunc(migrations, func(a, b migration) int {
		return cmp.Compare(a.Version, b.Version)
	})
	return migrations, nil
}

func (mg *Migrator) getMigrationsFromDB(ctx context.Context, tx *sqlx.Tx) ([]migration, error) {
	m := make([]migration, 0)
	query := fmt.Sprintf("SELECT version, up_script, down_script, error FROM %s ORDER BY version ASC", mg.tableName)
	err := tx.SelectContext(ctx, &m, query)
	if err != nil {
		return nil, fmt.Errorf("get migrations from DB: %w", err)
	}
	for _, val := range m {
		if val.MigrationError != "" {
			return nil, fmt.Errorf("migration %d is dirty: %s: %w", val.Version, val.MigrationError, errors.New("dirty migration"))
		}
	}
	return m, nil
}

func (mg *Migrator) insertMigrationToDB(ctx context.Context, tx *sqlx.Tx, m migration) error {
	p1 := mg.dialect.Placeholder(1)
	p2 := mg.dialect.Placeholder(2)
	p3 := mg.dialect.Placeholder(3)
	p4 := mg.dialect.Placeholder(4)
	query := fmt.Sprintf(`INSERT INTO %s (version, up_script, down_script, error) VALUES (%s, %s, %s, %s)`,
		mg.tableName, p1, p2, p3, p4)
	_, err := tx.ExecContext(ctx, query, m.Version, m.ContentUp, m.ContentDown, m.MigrationError)
	if err != nil {
		return fmt.Errorf("insert migration: %w", err)
	}
	return nil
}

func (mg *Migrator) deleteMigrationFromDB(ctx context.Context, tx *sqlx.Tx, m migration) error {
	p1 := mg.dialect.Placeholder(1)
	query := fmt.Sprintf("DELETE FROM %s WHERE version = %s", mg.tableName, p1)
	_, err := tx.ExecContext(ctx, query, m.Version)
	if err != nil {
		return fmt.Errorf("delete migration: %w", err)
	}
	return nil
}

func (mg *Migrator) markMigrationAsDirtyToDB(ctx context.Context, tx *sqlx.Tx, m migration) error {
	p1 := mg.dialect.Placeholder(1)
	p2 := mg.dialect.Placeholder(2)
	query := fmt.Sprintf("UPDATE %s SET error = %s WHERE version = %s", mg.tableName, p1, p2)
	_, err := tx.ExecContext(ctx, query, m.MigrationError, m.Version)
	if err != nil {
		return fmt.Errorf("mark migration as dirty: %w", err)
	}
	return nil
}

func execMigration(ctx context.Context, tx *sqlx.Tx, query string) error {
	_, err := tx.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("exec migration: %w", err)
	}
	return nil
}

func getDiff(sl1, sl2 []migration) ([]migration, []migration) {
	if len(sl1) >= len(sl2) {
		for i := 0; i < len(sl1); i++ {
			if len(sl2) == i {
				return sl1[i:], nil
			}
			if sl1[i].Version != sl2[i].Version {
				return sl1[i:], sl2[i:]
			}
		}
	}

	if len(sl1) < len(sl2) {
		for i := 0; i < len(sl2); i++ {
			if len(sl1) == i {
				return nil, sl2[i:]
			}
			if sl1[i].Version != sl2[i].Version {
				return sl1[i:], sl2[i:]
			}
		}
	}
	return nil, nil
}

func (mg *Migrator) UpdateMigrationList(ctx context.Context) error {
	migrations, err := mg.getMigrationsFromFiles()
	if err != nil {
		return err
	}
	return mg.updateMigrations(ctx, migrations)
}

func (mg *Migrator) updateMigrations(ctx context.Context, migrations []migration) error {
	migrationTableExist, err := mg.tableExist(ctx, mg.tableName)
	if err != nil {
		return fmt.Errorf("check %s table: %w", mg.tableName, err)
	}

	schemaMigrationsTableExist, err := mg.tableExist(ctx, mg.legacyTableName)
	if err != nil {
		return fmt.Errorf("check %s table: %w", mg.legacyTableName, err)
	}

	if migrationTableExist {
		return nil
	}

	_, err = mg.db.ExecContext(ctx, mg.dialect.CreateMigrationsTableSQL(mg.tableName))
	if err != nil {
		return fmt.Errorf("create table %s: %w", mg.tableName, err)
	}

	if !schemaMigrationsTableExist {
		return nil
	}

	tx, err := mg.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	schemeNumber, err := mg.getLastMigration(ctx, tx)
	if err != nil {
		return fmt.Errorf("get last migration: %w", err)
	}

	if !slices.ContainsFunc(migrations, func(a migration) bool {
		return a.Version == schemeNumber
	}) {
		return fmt.Errorf("migration not found: %d", schemeNumber)
	}

	for _, m := range migrations {
		if m.Version <= schemeNumber {
			if err := mg.insertMigrationToDB(ctx, tx, m); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (mg *Migrator) tableExist(ctx context.Context, tableName string) (bool, error) {
	var exist []bool
	err := mg.db.SelectContext(ctx, &exist, mg.dialect.TableExistsSQL(tableName))
	if err != nil {
		return false, fmt.Errorf("check table: %w", err)
	}
	if len(exist) == 0 {
		return false, nil
	}
	return exist[0], nil
}

func (mg *Migrator) getLastMigration(ctx context.Context, tx *sqlx.Tx) (int, error) {
	var lastScheme []int
	err := sqlx.SelectContext(ctx, tx, &lastScheme, mg.dialect.GetLastMigrationSQL(mg.legacyTableName))
	if err != nil {
		return 0, fmt.Errorf("internal DB error: %w", err)
	}

	if len(lastScheme) == 0 {
		return 0, errors.New("wrong data: internal DB error")
	}
	return lastScheme[0], nil
}

func (mg *Migrator) lockMigrationTable(ctx context.Context) (*sqlx.Tx, error) {
	tx, err := mg.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}

	lockSQL := mg.dialect.LockTableSQL(mg.tableName)
	if lockSQL != "" {
		_, err = tx.Exec(lockSQL)
		if err != nil {
			return nil, err
		}
	}

	return tx, nil
}
