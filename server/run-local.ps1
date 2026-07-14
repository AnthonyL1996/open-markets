# Local in-game testing launcher for openmarketsd (this machine has no `make`).
# Operator console ON, rate limiting OFF: the game client and the /console browser share one per-IP rate
# bucket on localhost, so the default 120/min throttles the client (429) and the Members tab / league name
# silently fail to load. Production keeps the 120 default. Mirrors the Makefile `run-local` target.
#   Usage:  ./run-local.ps1            (from the server/ directory)
$env:OM_CONSOLE = "1"
$env:OM_RATE_PER_MIN = "0"
# Account-creation limit OFF for local testing (default is 10/hr/IP): spinning up several test cities in one
# session would otherwise 429 on POST /accounts. Production keeps the 10/hr default.
$env:OM_ACCT_PER_HOUR = "0"
# Due-clock period. The shipped default is 2700s (≈ one in-game day); override to a fast value locally so
# trade/bond installments come due and cycle observably within a single test session instead of every 45 min.
$env:OM_DUE_INTERVAL_SEC = "120"
go run ./cmd/openmarketsd
