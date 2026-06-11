package migrate

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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
	logger          *slog.Logger
	dryRun          bool
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

func parseMigrationFile(name string) (version int, versionStr string, ok bool) {
	// Expected format: {version}_{name}.up.sql or {version}_{name}.down.sql
	// Remove the direction suffix first
	var stem string
	switch {
	case strings.HasSuffix(name, ".up.sql"):
		stem = strings.TrimSuffix(name, ".up.sql")
	case strings.HasSuffix(name, ".down.sql"):
		stem = strings.TrimSuffix(name, ".down.sql")
	default:
		return 0, "", false
	}

	// Find the first underscore to separate version from name
	idx := strings.Index(stem, "_")
	if idx <= 0 {
		return 0, "", false
	}

	versionStr = stem[:idx]
	n, err := strconv.Atoi(versionStr)
	if err != nil {
		return 0, "", false
	}

	return n, versionStr, true
}

func (mg *Migrator) getMigrationsFromFiles() ([]migration, error) {
	entries, err := fs.Glob(mg.fs, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("failed to list migration files: %w", err)
	}

	if len(entries) == 0 {
		return nil, errors.New("no migration files found")
	}

	// Separate up and down migrations
	upFiles := make(map[string]string) // version -> filename
	downFiles := make(map[string]string)

	for _, entry := range entries {
		version, verStr, ok := parseMigrationFile(entry)
		if !ok {
			continue
		}
		if strings.HasSuffix(entry, ".up.sql") {
			if _, dup := upFiles[verStr]; dup {
				return nil, fmt.Errorf("duplicate up migration for version %s: %s", verStr, entry)
			}
			upFiles[verStr] = entry
		} else if strings.HasSuffix(entry, ".down.sql") {
			if _, dup := downFiles[verStr]; dup {
				return nil, fmt.Errorf("duplicate down migration for version %s: %s", verStr, entry)
			}
			downFiles[verStr] = entry
		}
		_ = version
	}

	if len(upFiles) != len(downFiles) {
		return nil, fmt.Errorf("mismatched migration pairs: %d up files, %d down files", len(upFiles), len(downFiles))
	}

	// Collect all version strings and sort them numerically
	type versionEntry struct {
		version    int
		versionStr string
	}

	versions := make([]versionEntry, 0, len(upFiles))
	for verStr := range upFiles {
		n, err := strconv.Atoi(verStr)
		if err != nil {
			return nil, fmt.Errorf("invalid version number: %s", verStr)
		}
		versions = append(versions, versionEntry{version: n, versionStr: verStr})
	}
	slices.SortFunc(versions, func(a, b versionEntry) int {
		return cmp.Compare(a.version, b.version)
	})

	migrations := make([]migration, 0, len(versions))
	for _, ve := range versions {
		upFile := upFiles[ve.versionStr]
		downFile := downFiles[ve.versionStr]

		b, err := fs.ReadFile(mg.fs, upFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read up migration %s: %w", upFile, err)
		}
		c, err := fs.ReadFile(mg.fs, downFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read down migration %s: %w", downFile, err)
		}

		migrations = append(migrations, migration{
			Version:     ve.version,
			ContentUp:   string(b),
			ContentDown: string(c),
		})
	}
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

// ForceSetVersion manually inserts a version into the migrations table as if it
// was completed. This is useful for repairing a dirty migration state.
// It does NOT execute any migration SQL — it only records the version.
func (mg *Migrator) ForceSetVersion(ctx context.Context, version int, upScript, downScript string) error {
	if mg.dialect == nil {
		return errors.New("migrator: dialect is not set")
	}
	if mg.dryRun {
		mg.log("dry-run: would set version %d", version)
		return nil
	}
	m := migration{
		Version:     version,
		ContentUp:   upScript,
		ContentDown: downScript,
	}
	tx, err := mg.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("force set version: begin tx: %w", err)
	}
	if err := mg.insertMigrationToDB(ctx, tx, m); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ForceDown rolls back the last N migrations without checking errors strictly.
// It deletes migration records from DB even if the down SQL fails (best-effort).
// If N is 0 or negative, no action is taken.
func (mg *Migrator) ForceDown(ctx context.Context, n int) error {
	if mg.dialect == nil {
		return errors.New("migrator: dialect is not set")
	}
	if n <= 0 {
		return nil
	}

	p1 := mg.dialect.Placeholder(1)
	query := fmt.Sprintf("SELECT version, up_script, down_script, error FROM %s ORDER BY version DESC LIMIT %s", mg.tableName, p1)

	// Get migrations from DB
	var m []migration
	if err := mg.db.SelectContext(ctx, &m, query, n); err != nil {
		return fmt.Errorf("force down: get migrations: %w", err)
	}

	for i := range m {
		if mg.dryRun {
			mg.log("dry-run: would rollback version %d (SQL: %s)", m[i].Version, m[i].ContentDown)
			mg.log("dry-run: would delete version %d from tracking table", m[i].Version)
			continue
		}

		// Use a separate transaction for each migration to ensure best-effort cleanup
		tx, txErr := mg.db.BeginTxx(ctx, nil)
		if txErr != nil {
			return fmt.Errorf("force down: begin tx: %w", txErr)
		}

		_ = execMigration(ctx, tx, m[i].ContentDown)
		if err := mg.deleteMigrationFromDB(ctx, tx, m[i]); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("force down: commit tx: %w", err)
		}
		mg.log("force-down: rolled back version %d (errors ignored)", m[i].Version)
	}
	return nil
}

// DB returns the underlying database connection.
func (mg *Migrator) DB() *sqlx.DB {
	return mg.db
}

// MigrationStatus represents the status of a single migration.
type MigrationStatus struct {
	Version int
	Status  string // "applied", "pending", "dirty"
	Error   string
}

// Status returns the status of all migrations (both from DB and files).
func (mg *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	if mg.dialect == nil {
		return nil, errors.New("migrator: dialect is not set")
	}

	// Get migrations from DB directly (not in a transaction)
	var dbMigrations []migration
	query := fmt.Sprintf("SELECT version, up_script, down_script, error FROM %s ORDER BY version ASC", mg.tableName)
	if err := mg.db.SelectContext(ctx, &dbMigrations, query); err != nil {
		// If the table doesn't exist, treat as no migrations applied
		dbMigrations = nil
	}

	// Get migrations from files
	fsMigrations, err := mg.getMigrationsFromFiles()
	if err != nil {
		return nil, err
	}

	// Build a map of DB migrations
	dbMap := make(map[int]migration)
	for _, m := range dbMigrations {
		dbMap[m.Version] = m
	}

	var statuses []MigrationStatus
	// For all file migrations, determine status
	for _, fm := range fsMigrations {
		s := MigrationStatus{Version: fm.Version}
		if dm, exists := dbMap[fm.Version]; exists {
			if dm.MigrationError != "" {
				s.Status = "dirty"
				s.Error = dm.MigrationError
			} else {
				s.Status = "applied"
			}
		} else {
			s.Status = "pending"
		}
		statuses = append(statuses, s)
	}
	return statuses, nil
}

// CurrentVersion returns the latest applied migration version.
// Returns 0 if no migrations have been applied.
func (mg *Migrator) CurrentVersion(ctx context.Context) (int, error) {
	if mg.dialect == nil {
		return 0, errors.New("migrator: dialect is not set")
	}

	var maxVersion *int
	query := fmt.Sprintf("SELECT MAX(version) FROM %s", mg.tableName)
	err := mg.db.GetContext(ctx, &maxVersion, query)
	if err != nil {
		return 0, fmt.Errorf("get current version: %w", err)
	}
	if maxVersion == nil {
		return 0, nil
	}
	return *maxVersion, nil
}

func (mg *Migrator) log(format string, args ...any) {
	if mg.logger != nil {
		mg.logger.Info(fmt.Sprintf(format, args...))
	}
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
