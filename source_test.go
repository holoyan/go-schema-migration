package migrate

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestLoadSource_PairsUpAndDown(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql":   {Data: []byte("CREATE TABLE a();")},
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
		"20260102000000_b.up.sql":   {Data: []byte("CREATE TABLE b();")},
		"20260102000000_b.down.sql": {Data: []byte("DROP TABLE b;")},
	}
	got, err := loadFromFS(fs)
	if err != nil {
		t.Fatalf("loadFromFS: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 migrations, got %d", len(got))
	}
	if got[0].Name != "20260101000000_a" || got[1].Name != "20260102000000_b" {
		t.Fatalf("order wrong: %+v", got)
	}
	if got[0].UpSQL != "CREATE TABLE a();" || got[0].DownSQL != "DROP TABLE a;" {
		t.Fatalf("SQL contents wrong: %+v", got[0])
	}
}

func TestLoadSource_OrphanDownErrors(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.down.sql": {Data: []byte("DROP TABLE a;")},
	}
	_, err := loadFromFS(fs)
	if !errors.Is(err, ErrOrphanDownFile) {
		t.Fatalf("want ErrOrphanDownFile, got %v", err)
	}
}

func TestLoadSource_MissingDownAllowed(t *testing.T) {
	fs := fstest.MapFS{
		"20260101000000_a.up.sql": {Data: []byte("CREATE TABLE a();")},
	}
	got, err := loadFromFS(fs)
	if err != nil {
		t.Fatalf("loadFromFS: %v", err)
	}
	if got[0].DownSQL != "" {
		t.Fatalf("missing down file should yield empty DownSQL, got %q", got[0].DownSQL)
	}
}

func TestLoadSource_RejectsInvalidName(t *testing.T) {
	fs := fstest.MapFS{
		"not_a_migration.txt": {Data: []byte("ignored?")},
	}
	_, err := loadFromFS(fs)
	if !errors.Is(err, ErrInvalidMigrationName) {
		t.Fatalf("want ErrInvalidMigrationName, got %v", err)
	}
}

func TestLoadSource_SortsLexically(t *testing.T) {
	fs := fstest.MapFS{
		"20260103000000_c.up.sql": {Data: []byte("")},
		"20260101000000_a.up.sql": {Data: []byte("")},
		"20260102000000_b.up.sql": {Data: []byte("")},
	}
	got, err := loadFromFS(fs)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	want := []string{"20260101000000_a", "20260102000000_b", "20260103000000_c"}
	for i := range names {
		if names[i] != want[i] {
			t.Fatalf("want sorted %v, got %v", want, names)
		}
	}
}

func TestParseMigrationFilename(t *testing.T) {
	tests := []struct {
		in        string
		wantName  string
		wantDir   direction
		wantError bool
	}{
		{"20260416143052_add_users.up.sql", "20260416143052_add_users", dirUp, false},
		{"20260416143052_add_users.down.sql", "20260416143052_add_users", dirDown, false},
		{"20260416143052_multi_word_name.up.sql", "20260416143052_multi_word_name", dirUp, false},
		{"20260416143052_with_123_nums.up.sql", "20260416143052_with_123_nums", dirUp, false},
		// failure cases
		{"no_timestamp.up.sql", "", "", true},
		{"2026041614305_too_short.up.sql", "", "", true},  // 13 digits
		{"202604161430521_too_long.up.sql", "", "", true}, // 15 digits
		{"20260416143052_Upper.up.sql", "", "", true},     // uppercase
		{"20260416143052_name.sideways.sql", "", "", true},
		{"20260416143052_name.up.SQL", "", "", true}, // uppercase ext
		{"20260416143052_name.sql", "", "", true},    // missing up/down
		{".up.sql", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			gotName, gotDir, err := parseMigrationFilename(tc.in)
			if tc.wantError {
				if !errors.Is(err, ErrInvalidMigrationName) {
					t.Fatalf("want ErrInvalidMigrationName, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotName != tc.wantName {
				t.Fatalf("name: want %q got %q", tc.wantName, gotName)
			}
			if gotDir != tc.wantDir {
				t.Fatalf("dir: want %q got %q", tc.wantDir, gotDir)
			}
		})
	}
}
