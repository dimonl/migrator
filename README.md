# migrator

**migrator** is a lightweight, multi-database Go migration library with a built-in CLI. It manages paired `*.up.sql` / `*.down.sql` migration files for PostgreSQL, MySQL, and SQLite3.

[![Go Reference](https://pkg.go.dev/badge/github.com/dimonl/migrator.svg)](https://pkg.go.dev/github.com/dimonl/migrator)
[![Go Report Card](https://goreportcard.com/badge/github.com/dimonl/migrator)](https://goreportcard.com/report/github.com/dimonl/migrator)

---

## Features

- **Multi-database support:** PostgreSQL, MySQL, SQLite3
- **Automatic dialect detection** — no need to specify the driver manually in most cases
- **Dual mode:** use as a Go library or via the CLI
- **Migration pairs:** `*.up.sql` / `*.down.sql` files with version-based ordering
- **Dirty migration detection** — stops on error, stores error text, prevents further migrations
- **Legacy migration support** — automatically migrates from `schema_migrations` to `migrations` table
- **Configurable table names** — customize tracking table names
- **Dry-run mode** — simulate migrations without executing
- **Logging** — optional `slog`-compatible logger
- **Force repair** — `ForceSetVersion` and `ForceDown` for recovery scenarios
- **CLI** — `up`, `down`, `create`, `status`, `version`, `force`, `init` commands

---

## Installation

### CLI

```bash
go install github.com/dimonl/migrator/cmd/migrator@latest
```

### Library

```bash
go get github.com/dimonl/migrator
```

---

## Migration File Format

Migrations are paired files in a single directory:

```
migrations/
├── 001_create_users.up.sql
├── 001_create_users.down.sql
├── 002_add_email.up.sql
├── 002_add_email.down.sql
└── ...
```

- Naming: `{version}_{description}.up.sql` / `{version}_{description}.down.sql`
- Versions can have leading zeros: `001`, `002`, etc.
- Versions must be unique across all files
- Each up file must have a matching down file with the same version

---

## CLI Usage

### Global Flags

| Flag              | Env Variable     | Default            | Description                            |
|-------------------|------------------|---------------------|----------------------------------------|
| `--dsn`           | `MIGRATOR_DSN`   | — (required)        | Database connection string             |
| `--driver`        | `MIGRATOR_DRIVER`| auto-detected       | `postgres`, `mysql`, `sqlite3`         |
| `--dir`           | —                | `./migrations`      | Directory with migration files         |
| `--table`         | —                | `migrations`        | Migrations tracking table name         |
| `--disable-down`  | —                | `false`             | Disable down migrations in production  |
| `--dry-run`       | —                | `false`             | Print SQL without executing            |
| `--verbose`, `-v` | —                | `false`             | Verbose output                         |

### Commands

#### `up`

Apply all pending migrations:

```bash
migrator --dsn "postgres://user:pass@localhost:5432/db?sslmode=disable" up
```

#### `down`

Rollback the last N migrations (default: 1):

```bash
migrator --dsn "postgres://user:pass@localhost:5432/db?sslmode=disable" down --steps 2
```

#### `create`

Create a new migration pair:

```bash
migrator create --name create_users_table
```

Creates `migrations/1_create_users_table.up.sql` and `migrations/1_create_users_table.down.sql`.

#### `status`

Show migration status (applied, pending, dirty):

```bash
migrator --dsn "postgres://user:pass@localhost:5432/db?sslmode=disable" status
```

#### `version`

Show the current migration version:

```bash
migrator --dsn "postgres://user:pass@localhost:5432/db?sslmode=disable" version
```

#### `force`

Manually mark a version as completed (repair dirty state):

```bash
migrator --dsn "postgres://user:pass@localhost:5432/db?sslmode=disable" force --version 3
```

#### `init`

Create the migrations tracking table without running any migrations:

```bash
migrator --dsn "postgres://user:pass@localhost:5432/db?sslmode=disable" init
```

### Examples

```bash
# PostgreSQL (auto-detect)
migrator --dsn "postgres://user:pass@localhost:5432/mydb?sslmode=disable" up

# MySQL
migrator --dsn "mysql://user:pass@tcp(localhost:3306)/mydb" --driver mysql up

# SQLite3
migrator --dsn "./data.db" --driver sqlite3 up

# With custom migration directory
migrator --dsn "postgres://..." --dir ./db/migrations up

# Dry-run
migrator --dsn "postgres://..." --dry-run up

# Verbose logging
migrator --dsn "postgres://..." -v up

# Disable down migrations (production safety)
migrator --dsn "postgres://..." --disable-down up
```

---

## Library Usage

### Basic Usage

```go
package main

import (
    "context"
    "embed"
    "log"

    "github.com/jmoiron/sqlx"
    "github.com/dimonl/migrator/migrate"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func main() {
    ctx := context.Background()

    db := sqlx.MustConnect("postgres", "postgres://user:pass@localhost:5432/db?sslmode=disable")

    mg := migrate.NewMigrator(db, migrationsFS)

    // Create migrations table if needed + run pending migrations
    if err := mg.UpdateMigrationList(ctx); err != nil {
        log.Fatal(err)
    }
    if err := mg.Migrate(ctx); err != nil {
        log.Fatal(err)
    }
}
```

### Using InitDB (convenience function)

```go
ctx := context.Background()
db, err := migrate.InitDB(ctx, "postgres://user:pass@localhost:5432/db?sslmode=disable", migrationsFS)
if err != nil {
    log.Fatal(err)
}
defer db.Close()
```

### With Options

```go
import (
    "log/slog"
    "github.com/dimonl/migrator/migrate"
)

mg := migrate.NewMigrator(
    db,
    migrationsFS,
    migrate.WithTableName("my_migrations"),
    migrate.WithLegacyTableName("old_migrations"),
    migrate.WithLogger(slog.Default()),
    migrate.WithDisabledDownMigrations(),
    migrate.WithDryRun(),
)
```

### Explicit Dialect

```go
import "github.com/dimonl/migrator/migrate"

dialect, _ := migrate.DetectDialect("mysql")
mg := migrate.NewMigrator(
    db,
    migrationsFS,
    migrate.WithDialect(dialect),
)
```

### Status & Current Version

```go
statuses, err := mg.Status(ctx)
for _, s := range statuses {
    fmt.Printf("Version %d: %s", s.Version, s.Status)
    if s.Error != "" {
        fmt.Printf(" (%s)", s.Error)
    }
}

version, err := mg.CurrentVersion(ctx)
fmt.Printf("Current version: %d\n", version)
```

### Force Repair

```go
// Mark version 3 as completed (repair dirty state)
mg.ForceSetVersion(ctx, 3, "", "")

// Rollback last 2 migrations ignoring errors
mg.ForceDown(ctx, 2)
```

### Using with File System (CLI-style)

```go
import "os"

fs := os.DirFS("./migrations")
mg := migrate.NewMigrator(db, fs)
```

---

## Supported Databases

| Database    | Placeholders | Table Existence Check              | Locking                              |
|-------------|--------------|------------------------------------|--------------------------------------|
| PostgreSQL  | `$1`, `$2`…  | `information_schema.tables`        | `LOCK TABLE ... IN SHARE ROW EXCLUSIVE MODE` |
| MySQL       | `?`          | `information_schema.tables`        | `LOCK TABLES ... WRITE`              |
| SQLite3     | `?`          | `sqlite_master`                    | Transaction-based (no explicit lock) |

Driver auto-detection works for:
- **PostgreSQL:** DSN containing `postgres://`, `postgresql://`, `host=`, or `sslmode=`
- **MySQL:** DSN containing `mysql://`, `@tcp(`, or `charset=`
- **SQLite3:** DSN ending with `.db`, `.sqlite`, `.sqlite3`, or `:memory:`

---

## Project Structure

```
migrator/
├── cmd/
│   └── migrator/
│       └── main.go                    # CLI entry point
├── migrate/
│   ├── migrate.go                     # Core migration logic
│   ├── migrate_test.go                # Tests
│   ├── dialect.go                     # Dialect interface + factory
│   ├── dialect_postgres.go            # PostgreSQL implementation
│   ├── dialect_mysql.go               # MySQL implementation
│   ├── dialect_sqlite.go              # SQLite implementation
│   ├── option.go                      # Option types & functions
│   └── testdata/
│       ├── test_data.go               # Embedded test migrations
│       ├── 1_migration_1.up.sql
│       ├── 1_migration_1.down.sql
│       ├── 2_migration_2.up.sql
│       ├── 2_migration_2.down.sql
│       ├── 3_migration_3.up.sql
│       └── 3_migration_3.down.sql
├── go.mod
├── go.sum
├── .gitignore
├── library.md                         # Implementation notes
└── README.md                          # This file
```

---

## License

MIT