package db

import (
	"context"
	"strings"
	"testing"
)

func TestMigrateDoesNotExposeInvalidDSNCredentials(t *testing.T) {
	err := Migrate(context.Background(), "postgres://user:private-password@host:invalid-port/database")
	if err == nil {
		t.Fatal("invalid DSN unexpectedly accepted")
	}
	if strings.Contains(err.Error(), "private-password") || err.Error() != "invalid DATABASE_URL" {
		t.Fatalf("unsafe DSN error: %v", err)
	}
}
