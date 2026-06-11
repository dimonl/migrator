package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/urfave/cli/v2"

	"migrator/migrate"
)

func main() {
	app := &cli.App{
		Name:        "migrator",
		Usage:       "Database migration tool",
		Description: "Run, rollback, and manage database migrations for PostgreSQL, MySQL, and SQLite.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "dsn",
				Usage:    "Database connection string",
				Required: true,
				EnvVars:  []string{"MIGRATOR_DSN"},
			},
			&cli.StringFlag{
				Name:    "driver",
				Usage:   "Database driver: postgres, mysql, sqlite3 (auto-detected from DSN if omitted)",
				EnvVars: []string{"MIGRATOR_DRIVER"},
			},
			&cli.StringFlag{
				Name:  "dir",
				Usage: "Directory with migration files",
				Value: "./migrations",
			},
			&cli.StringFlag{
				Name:  "table",
				Usage: "Migrations table name",
				Value: "migrations",
			},
			&cli.BoolFlag{
				Name:  "disable-down",
				Usage: "Disable down migrations in production",
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "Print SQL without executing",
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Verbose output",
			},
		},
		Before: func(cCtx *cli.Context) error {
			// Auto-detect driver from DSN if not explicitly set
			if cCtx.String("driver") == "" {
				dsn := cCtx.String("dsn")
				driver := detectDriverFromDSN(dsn)
				if driver == "" {
					return fmt.Errorf("could not auto-detect driver from DSN; use --driver flag")
				}
				cCtx.Set("driver", driver)
			}
			return nil
		},
		Commands: []*cli.Command{
			{
				Name:  "up",
				Usage: "Apply all pending migrations",
				Action: func(cCtx *cli.Context) error {
					return runUp(cCtx)
				},
			},
			{
				Name:  "down",
				Usage: "Rollback last N migrations",
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:    "steps",
						Aliases: []string{"n"},
						Usage:   "Number of migrations to rollback",
						Value:   1,
					},
				},
				Action: func(cCtx *cli.Context) error {
					return runDown(cCtx)
				},
			},
			{
				Name:  "create",
				Usage: "Create new migration files (up + down)",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "name",
						Aliases:  []string{"n"},
						Usage:    "Migration name (e.g. create_users_table)",
						Required: true,
					},
				},
				Action: func(cCtx *cli.Context) error {
					return runCreate(cCtx)
				},
			},
			{
				Name:  "status",
				Usage: "Show migration status",
				Action: func(cCtx *cli.Context) error {
					return runStatus(cCtx)
				},
			},
			{
				Name:  "version",
				Usage: "Show current migration version",
				Action: func(cCtx *cli.Context) error {
					return runVersion(cCtx)
				},
			},
			{
				Name:  "force",
				Usage: "Set version without running migration (repair)",
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:     "version",
						Aliases:  []string{"v"},
						Usage:    "Version number to set",
						Required: true,
					},
				},
				Action: func(cCtx *cli.Context) error {
					return runForce(cCtx)
				},
			},
			{
				Name:  "init",
				Usage: "Create migrations table only",
				Action: func(cCtx *cli.Context) error {
					return runInit(cCtx)
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func detectDriverFromDSN(dsn string) string {
	dsnLower := strings.ToLower(dsn)
	switch {
	case strings.Contains(dsnLower, "postgres://"),
		strings.Contains(dsnLower, "postgresql://"),
		strings.Contains(dsnLower, "host="),
		strings.Contains(dsnLower, "sslmode="):
		return "postgres"
	case strings.Contains(dsnLower, "mysql://"),
		strings.Contains(dsnLower, "@tcp("),
		strings.Contains(dsnLower, "charset="):
		return "mysql"
	case strings.HasSuffix(dsnLower, ".db"),
		strings.HasSuffix(dsnLower, ".sqlite"),
		strings.HasSuffix(dsnLower, ".sqlite3"),
		dsnLower == ":memory:":
		return "sqlite3"
	default:
		return ""
	}
}

func buildMigrator(cCtx *cli.Context) (*migrate.Migrator, error) {
	driver := cCtx.String("driver")
	dsn := cCtx.String("dsn")
	tableName := cCtx.String("table")

	dialect, err := migrate.DetectDialect(driver)
	if err != nil {
		return nil, fmt.Errorf("unsupported driver %q: %w", driver, err)
	}

	db, err := sqlx.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	dir := cCtx.String("dir")
	fs := os.DirFS(dir)

	var opts []migrate.Option
	opts = append(opts, migrate.WithDialect(dialect))
	opts = append(opts, migrate.WithTableName(tableName))

	if cCtx.Bool("disable-down") {
		opts = append(opts, migrate.WithDisabledDownMigrations())
	}
	if cCtx.Bool("dry-run") {
		opts = append(opts, migrate.WithDryRun())
	}
	if cCtx.Bool("verbose") {
		// In a real app, we'd use a proper logger; for now, verbose flags just enable more output
	}

	mg := migrate.NewMigrator(db, fs, opts...)
	return mg, nil
}

func runUp(cCtx *cli.Context) error {
	mg, err := buildMigrator(cCtx)
	if err != nil {
		return err
	}
	defer mg.DB().Close()

	if err := mg.UpdateMigrationList(cCtx.Context); err != nil {
		return fmt.Errorf("update migration list: %w", err)
	}
	if err := mg.Migrate(cCtx.Context); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	fmt.Println("All pending migrations applied successfully.")
	return nil
}

func runDown(cCtx *cli.Context) error {
	mg, err := buildMigrator(cCtx)
	if err != nil {
		return err
	}
	defer mg.DB().Close()

	steps := cCtx.Int("steps")
	if cCtx.Bool("dry-run") || cCtx.Bool("disable-down") {
		fmt.Printf("Dry-run: would rollback %d migration(s)\n", steps)
		return nil
	}

	if err := mg.ForceDown(cCtx.Context, steps); err != nil {
		return fmt.Errorf("down: %w", err)
	}
	fmt.Printf("Rolled back %d migration(s).\n", steps)
	return nil
}

func runCreate(cCtx *cli.Context) error {
	name := cCtx.String("name")
	dir := cCtx.String("dir")

	// Get the next version number
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read dir: %w", err)
	}

	nextVersion := 1
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) < 2 {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(parts[0], "%d", &v); err != nil {
			continue
		}
		if v >= nextVersion {
			nextVersion = v + 1
		}
	}

	// Ensure the directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	upFile := filepath.Join(dir, fmt.Sprintf("%d_%s.up.sql", nextVersion, name))
	downFile := filepath.Join(dir, fmt.Sprintf("%d_%s.down.sql", nextVersion, name))

	upContent := fmt.Sprintf("-- %s up\n", name)
	downContent := fmt.Sprintf("-- %s down\n", name)

	if err := os.WriteFile(upFile, []byte(upContent), 0644); err != nil {
		return fmt.Errorf("write up file: %w", err)
	}
	if err := os.WriteFile(downFile, []byte(downContent), 0644); err != nil {
		return fmt.Errorf("write down file: %w", err)
	}

	fmt.Printf("Created migration %d:\n", nextVersion)
	fmt.Printf("  %s\n", upFile)
	fmt.Printf("  %s\n", downFile)
	return nil
}

func runStatus(cCtx *cli.Context) error {
	mg, err := buildMigrator(cCtx)
	if err != nil {
		return err
	}
	defer mg.DB().Close()

	// Print status of all migrations
	statuses, err := mg.Status(cCtx.Context)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}

	if len(statuses) == 0 {
		fmt.Println("No migrations found.")
		return nil
	}

	fmt.Printf("%-10s %-20s %s\n", "Version", "Status", "Error")
	fmt.Println(strings.Repeat("-", 60))
	for _, s := range statuses {
		errorStr := ""
		if s.Error != "" {
			errorStr = s.Error
		}
		fmt.Printf("%-10d %-20s %s\n", s.Version, s.Status, errorStr)
	}
	return nil
}

func runVersion(cCtx *cli.Context) error {
	mg, err := buildMigrator(cCtx)
	if err != nil {
		return err
	}
	defer mg.DB().Close()

	version, err := mg.CurrentVersion(cCtx.Context)
	if err != nil {
		return fmt.Errorf("version: %w", err)
	}

	if version == 0 {
		fmt.Println("No migrations have been applied.")
	} else {
		fmt.Printf("Current migration version: %d\n", version)
	}
	return nil
}

func runForce(cCtx *cli.Context) error {
	mg, err := buildMigrator(cCtx)
	if err != nil {
		return err
	}
	defer mg.DB().Close()

	version := cCtx.Int("version")

	if cCtx.Bool("dry-run") {
		fmt.Printf("Dry-run: would force-set version %d\n", version)
		return nil
	}

	// Use empty scripts for force-set
	if err := mg.ForceSetVersion(cCtx.Context, version, "", ""); err != nil {
		return fmt.Errorf("force: %w", err)
	}
	fmt.Printf("Version %d set as completed.\n", version)
	return nil
}

func runInit(cCtx *cli.Context) error {
	mg, err := buildMigrator(cCtx)
	if err != nil {
		return err
	}
	defer mg.DB().Close()

	if err := mg.UpdateMigrationList(cCtx.Context); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	fmt.Println("Migrations table created successfully.")
	return nil
}
