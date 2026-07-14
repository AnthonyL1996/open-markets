// Command omsim runs the randomized economy simulation harness and prints the invariant Result. It exits
// non-zero if any system-level invariant (cash conservation, austerity escapability, no stranded
// trades/bonds, no overflow, full drain) was violated — usable as a local stress check or a CI gate.
//
//	go run ./cmd/omsim -members 7 -rounds 2000 -seed 42
//	go run ./cmd/omsim -sweep 100      # run seeds 1..100, fail on the first violation
package main

import (
	"flag"
	"fmt"
	"os"

	"openmarkets/server/internal/sim"
)

func main() {
	members := flag.Int("members", 5, "league members")
	rounds := flag.Int("rounds", 400, "random operation rounds")
	drain := flag.Int("drain", 4000, "max drain ticks before declaring an escapability failure")
	seed := flag.Int64("seed", 1, "RNG seed (deterministic per seed)")
	sweep := flag.Int("sweep", 0, "if >0, run seeds 1..N and fail on the first violation")
	flag.Parse()

	base := sim.Params{Members: *members, Rounds: *rounds, MaxDrainTicks: *drain, Seed: *seed}

	if *sweep > 0 {
		for s := int64(1); s <= int64(*sweep); s++ {
			p := base
			p.Seed = s
			res := sim.Run(p)
			if !res.OK {
				fmt.Printf("FAIL seed=%d %+v\n", s, res)
				os.Exit(1)
			}
		}
		fmt.Printf("OK: %d seeds held all invariants (members=%d rounds=%d)\n", *sweep, *members, *rounds)
		return
	}

	res := sim.Run(base)
	fmt.Printf("members=%d rounds=%d seed=%d\n", *members, *rounds, *seed)
	fmt.Printf("accounts=%d trades=%d bonds=%d drainTicks=%d fullyDrained=%v\n",
		res.Accounts, res.Trades, res.Bonds, res.DrainTicks, res.FullyDrained)
	fmt.Printf("conservationTotal=%d voidNetCents=%d strangerAccounts=%d\n",
		res.ConservationTotal, res.VoidNetCents, res.StrangerAccounts)
	fmt.Printf("stuckBondsDefaulted=%d stuckActiveTrades=%d austerityCities=%d settledOverflow=%d\n",
		res.StuckBondsDefaulted, res.StuckActiveTrades, res.AusterityCities, res.SettledOverflow)
	fmt.Printf("peakDefaultedBonds=%d peakAusterityCities=%d (hard paths exercised)\n",
		res.PeakDefaultedBonds, res.PeakAusterityCities)
	if res.OK {
		fmt.Println("OK: all invariants held")
		return
	}
	fmt.Println("FAIL: an invariant was violated")
	os.Exit(1)
}
