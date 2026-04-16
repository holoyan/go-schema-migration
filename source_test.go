package migrate

import (
	"errors"
	"testing"
)

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
