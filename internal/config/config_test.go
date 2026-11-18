package config

import (
	"testing"
	"time"
)

func TestLoadValidatesTimeoutsAndRequiredDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("missing database accepted")
	}
	t.Setenv("DATABASE_URL", "postgres://labreserve@localhost/labreserve")
	for _, name := range []string{"DATABASE_TIMEOUT", "DIAGNOSTICS_TIMEOUT", "HTTP_REQUEST_TIMEOUT", "SHUTDOWN_TIMEOUT"} {
		for _, value := range []string{"oops", "0s", "-1s", "2m"} {
			t.Run(name+value, func(t *testing.T) {
				t.Setenv(name, value)
				if _, err := Load(); err == nil {
					t.Fatal("invalid timeout accepted")
				}
			})
		}
	}
	t.Setenv("DATABASE_TIMEOUT", "150ms")
	c, err := Load()
	if err != nil || c.DatabaseTimeout != 150*time.Millisecond {
		t.Fatalf("config %+v err %v", c, err)
	}
}
