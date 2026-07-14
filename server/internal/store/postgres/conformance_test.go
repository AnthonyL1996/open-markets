//go:build embeddedpg

package postgres

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"openmarkets/server/internal/store/storeconf"
)

// TestConformance_Postgres runs the shared store conformance scenario against a real Postgres. It needs a DB, so
// it SKIPS cleanly by default (keeping `go test ./...` fast + offline). Two opt-in ways to run it:
//
//	# (a) point at any reachable Postgres (use a throwaway DB — the run TRUNCATEs its tables):
//	TEST_DB_URL=postgres://postgres:postgres@localhost:5432/omtest?sslmode=disable go test ./internal/store/postgres/ -run Conformance -v
//
//	# (b) no Docker, no external server — spin up an embedded native Postgres just for the test:
//	OM_EMBEDDED_PG=1 go test ./internal/store/postgres/ -run Conformance -v
//
// (b) downloads a small native Postgres binary once (cached under the temp dir) and runs it in-process on a free
// port — no Docker/WSL. The run resets to a clean schema first, so repeated invocations are independent.
func TestConformance_Postgres(t *testing.T) {
	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		if os.Getenv("OM_EMBEDDED_PG") == "" {
			t.Skip("set TEST_DB_URL (a reachable postgres:// URL) or OM_EMBEDDED_PG=1 (embedded native Postgres) to run")
		}
		var stop func()
		dbURL, stop = startEmbeddedPostgres(t)
		defer stop()
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

// startEmbeddedPostgres launches a native Postgres (no Docker) on a free port and returns its DSN + a stop func.
// The downloaded binary archive is cached under the OS temp dir so subsequent runs skip the download; the data
// dir is per-run (isolated). Fatals on any startup error.
func startEmbeddedPostgres(t *testing.T) (string, func()) {
	t.Helper()
	port := freePort(t)
	cache := filepath.Join(os.TempDir(), "om-embedded-pg")
	epg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(uint32(port)).
		RuntimePath(filepath.Join(t.TempDir(), "rt")).
		DataPath(filepath.Join(t.TempDir(), "data")).
		BinariesPath(filepath.Join(cache, "bin")).
		CachePath(filepath.Join(cache, "cache")).
		Logger(io.Discard).
		StartTimeout(120 * time.Second))
	if err := epg.Start(); err != nil {
		t.Fatalf("start embedded postgres: %v", err)
	}
	dsn := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)
	return dsn, func() { _ = epg.Stop() }
}

// freePort asks the OS for an ephemeral TCP port, then frees it for embedded Postgres to bind. The tiny
// close→rebind race is acceptable for a single local test process.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
