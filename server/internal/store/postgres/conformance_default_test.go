//go:build !embeddedpg

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"openmarkets/server/internal/store/storeconf"
)

// TestConformance_Postgres runs against TEST_DB_URL when provided and otherwise skips by default. The embedded
// native-Postgres variant lives behind the embeddedpg build tag so the default offline `go test ./...` gate does
// not need to download github.com/fergusstrange/embedded-postgres.
func TestConformance_Postgres(t *testing.T) {
	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		if os.Getenv("OM_EMBEDDED_PG") != "" {
			t.Skip("OM_EMBEDDED_PG requires `go test -tags embeddedpg ./internal/store/postgres`")
		}
		t.Skip("set TEST_DB_URL (a reachable postgres:// URL) to run")
	}

	storeconf.Run(t, func(t *testing.T) storeconf.Store {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pg, err := New(ctx, dbURL)
		if err != nil {
			t.Fatalf("connect postgres (%s): %v", dbURL, err)
		}
		t.Cleanup(pg.Close)
		if err := resetSchema(ctx, pg); err != nil {
			t.Fatalf("reset schema: %v", err)
		}
		return pg
	})
}
