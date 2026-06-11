package migrate

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"

	"migrator/migrate/testdata"
)

func TestGetMigrationsFromFiles(t *testing.T) {
	t.Parallel()
	migrator := Migrator{
		db: nil,
		fs: testdata.FS,
	}

	fl, _ := fs.Glob(migrator.fs, "*.up.sql")
	dt, err := migrator.getMigrationsFromFiles()
	require.NoError(t, err)
	require.Equal(t, len(fl), len(dt))
}

func TestGetDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		migrationsDB []migration
		migrationsFS []migration
		diffBD       []migration
		diffFS       []migration
	}{
		{
			name: "TestGetDiff_NoDiff",
			migrationsDB: []migration{
				{
					Version:     1,
					ContentUp:   "1",
					ContentDown: "1",
				},
				{
					Version:     2,
					ContentUp:   "2",
					ContentDown: "2",
				},
				{
					Version:     3,
					ContentUp:   "3",
					ContentDown: "3",
				},
			},
			migrationsFS: []migration{
				{
					Version:     1,
					ContentUp:   "1",
					ContentDown: "1",
				},
				{
					Version:     2,
					ContentUp:   "2",
					ContentDown: "2",
				},
				{
					Version:     3,
					ContentUp:   "3",
					ContentDown: "3",
				},
			},
			diffBD: nil,
			diffFS: nil,
		},
		{
			name: "TestGetDiff_HasDiffFromFS",
			migrationsDB: []migration{
				{
					Version:     1,
					ContentUp:   "1",
					ContentDown: "1",
				},
				{
					Version:     2,
					ContentUp:   "2",
					ContentDown: "2",
				},
				{
					Version:     3,
					ContentUp:   "3",
					ContentDown: "3",
				},
			},
			migrationsFS: []migration{
				{
					Version:     1,
					ContentUp:   "1",
					ContentDown: "1",
				},
				{
					Version:     2,
					ContentUp:   "2",
					ContentDown: "2",
				},
				{
					Version:     3,
					ContentUp:   "3",
					ContentDown: "3",
				},
				{
					Version:     5,
					ContentUp:   "5",
					ContentDown: "5",
				},
			},
			diffBD: nil,
			diffFS: []migration{
				{
					Version:     5,
					ContentUp:   "5",
					ContentDown: "5",
				},
			},
		},
		{
			name: "TestGetDiff_HasDiffFromDB",
			migrationsDB: []migration{
				{
					Version:     1,
					ContentUp:   "1",
					ContentDown: "1",
				},
				{
					Version:     2,
					ContentUp:   "2",
					ContentDown: "2",
				},
				{
					Version:     3,
					ContentUp:   "3",
					ContentDown: "3",
				},
				{
					Version:     5,
					ContentUp:   "5",
					ContentDown: "5",
				},
			},
			migrationsFS: []migration{
				{
					Version:     1,
					ContentUp:   "1",
					ContentDown: "1",
				},
				{
					Version:     2,
					ContentUp:   "2",
					ContentDown: "2",
				},
				{
					Version:     3,
					ContentUp:   "3",
					ContentDown: "3",
				},
			},
			diffBD: []migration{
				{
					Version:     5,
					ContentUp:   "5",
					ContentDown: "5",
				},
			},
			diffFS: nil,
		},
		{
			name: "TestGetDiff_HasDiffFromBothSides",
			migrationsDB: []migration{
				{
					Version:     1,
					ContentUp:   "1",
					ContentDown: "1",
				},
				{
					Version:     2,
					ContentUp:   "2",
					ContentDown: "2",
				},
				{
					Version:     3,
					ContentUp:   "3",
					ContentDown: "3",
				},
				{
					Version:     5,
					ContentUp:   "5",
					ContentDown: "5",
				},
			},
			migrationsFS: []migration{
				{
					Version:     1,
					ContentUp:   "1",
					ContentDown: "1",
				},
				{
					Version:     2,
					ContentUp:   "2",
					ContentDown: "2",
				},
				{
					Version:     3,
					ContentUp:   "3",
					ContentDown: "3",
				},
				{
					Version:     6,
					ContentUp:   "6",
					ContentDown: "6",
				},
			},
			diffBD: []migration{
				{
					Version:     5,
					ContentUp:   "5",
					ContentDown: "5",
				},
			},
			diffFS: []migration{
				{
					Version:     6,
					ContentUp:   "6",
					ContentDown: "6",
				},
			},
		},
		{
			name: "TestGetDiff_HasRandomDiffFromBothSides",
			migrationsDB: []migration{
				{
					Version:        1,
					ContentUp:      "1",
					ContentDown:    "1",
					MigrationError: "1",
				},
				{
					Version:        3,
					ContentUp:      "3",
					ContentDown:    "3",
					MigrationError: "3",
				},
			},
			migrationsFS: []migration{
				{
					Version:        1,
					ContentUp:      "1",
					ContentDown:    "1",
					MigrationError: "1",
				},
				{
					Version:        2,
					ContentUp:      "2",
					ContentDown:    "2",
					MigrationError: "2",
				},
				{
					Version:        3,
					ContentUp:      "3",
					ContentDown:    "3",
					MigrationError: "3",
				},
			},
			diffBD: []migration{
				{
					Version:        3,
					ContentUp:      "3",
					ContentDown:    "3",
					MigrationError: "3",
				},
			},
			diffFS: []migration{
				{
					Version:        2,
					ContentUp:      "2",
					ContentDown:    "2",
					MigrationError: "2",
				},
				{
					Version:        3,
					ContentUp:      "3",
					ContentDown:    "3",
					MigrationError: "3",
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			diffBD, diffFS := getDiff(tt.migrationsDB, tt.migrationsFS)
			require.Equal(t, tt.diffBD, diffBD)
			require.Equal(t, tt.diffFS, diffFS)
		})
	}
}
