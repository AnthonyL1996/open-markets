.PHONY: run run-local build test race cover vet fmt fmt-check verify sim fuzz clean docker

run:
	go run ./cmd/openmarketsd

# run-local is the local in-game testing launch: operator console ON, rate limiting OFF. The game client and
# the /console browser hit the server from the same localhost IP and share one per-IP rate bucket, so the
# default 120/min throttles the client (429) and the Members tab / league name silently fail to load.
# Production keeps the 120 default. See memory om-local-test-rate-limit.
run-local:
	OM_CONSOLE=1 OM_RATE_PER_MIN=0 go run ./cmd/openmarketsd

# verify is the full pre-merge / CI gate: formatting, vet, the race suite, a multi-seed economy stress sweep,
# and a short fuzz pass over the money math. Pure Go — no game DLLs needed (the net35 client builds on Windows
# or with the 4 Managed DLLs; see README).
verify: fmt-check vet race sim fuzz

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# sim runs the randomized-economy invariant harness across many seeds (cash conservation, austerity
# escapability, no stranded/overflow state). Fails non-zero on the first violation.
sim:
	go run ./cmd/omsim -sweep 200

# fuzz runs each money-math fuzz target for a short, fixed budget (enough to catch regressions in CI without
# unbounded runtime). Bump FUZZTIME locally to hunt harder.
FUZZTIME ?= 10s
fuzz:
	go test ./internal/money -run=^$$ -fuzz=^FuzzAmortize$$          -fuzztime=$(FUZZTIME)
	go test ./internal/money -run=^$$ -fuzz=^FuzzTotalDueCents$$     -fuzztime=$(FUZZTIME)
	go test ./internal/money -run=^$$ -fuzz=^FuzzLineValueCents$$    -fuzztime=$(FUZZTIME)
	go test ./internal/money -run=^$$ -fuzz=^FuzzValidateBondTerms$$ -fuzztime=$(FUZZTIME)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/openmarketsd ./cmd/openmarketsd

test:
	go test ./...

race:
	go test -race ./...

cover:
	go test -cover ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin data

docker:
	docker build -t openmarketsd:latest .
