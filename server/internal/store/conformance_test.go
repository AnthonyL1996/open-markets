package store_test

import (
	"testing"

	"openmarkets/server/internal/store"
	"openmarkets/server/internal/store/storeconf"
)

// TestConformance_Memory runs the shared store conformance scenario against the in-memory store ALWAYS. This
// validates the suite itself here (the Postgres run, in internal/store/postgres, is gated on TEST_DB_URL).
//
// To run the suite against a live Postgres:
//
//	TEST_DB_URL=postgres://user:pass@host:5432/db go test ./internal/store/postgres/ -run Conformance
func TestConformance_Memory(t *testing.T) {
	storeconf.Run(t, func(t *testing.T) storeconf.Store {
		return store.NewMemory("")
	})
}
