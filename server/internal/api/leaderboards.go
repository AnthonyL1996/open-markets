package api

import (
	"math"
	"net/http"
	"sort"

	"openmarkets/server/internal/store"
)

// Leaderboards (M-leaderboards): read-only, additive standings derived from the existing economy state. Two
// surfaces: a per-league board set (member-only, full identities + city-reported vitals + fun "titles"), and a
// privacy-preserving global board set (any authenticated account, server-authoritative metrics only, anonymized
// other players + percentile/tier). Nothing here mutates money/settlement/bond/trade state — every value is a
// pure read of AuditLeague / TradesFor / InvestmentHistory / reports / bonds.

// boardRow is one ranked entry in a per-league board.
type boardRow struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName,omitempty"`
	Value       int64  `json:"value"`
	Rank        int    `json:"rank"`
}

// boardDTO is a single named, ranked board.
type boardDTO struct {
	ID             string     `json:"id"`
	Label          string     `json:"label"`
	HigherIsBetter bool       `json:"higherIsBetter"`
	Rows           []boardRow `json:"rows"`
}

// globalBoardRow is one ranked entry in a global (anonymized) board.
type globalBoardRow struct {
	Rank        int    `json:"rank"`
	DisplayName string `json:"displayName"`
	Value       int64  `json:"value"`
	Percentile  int    `json:"percentile"`
	Tier        string `json:"tier"`
	You         bool   `json:"you,omitempty"`
}

// titleEntry is one member's awarded titles. Emitted as an ARRAY (not a map keyed by account id): the net35
// client's JSON reader binds onto DTO fields by name and can't bind a dynamic-key object, so a list of
// {accountId, titles[]} parses cleanly where a map would not.
type titleEntry struct {
	AccountID string   `json:"accountId"`
	Titles    []string `json:"titles"`
}

// globalBoardDTO is a single global board (top 100 + the caller's own row).
type globalBoardDTO struct {
	ID             string           `json:"id"`
	Label          string           `json:"label"`
	HigherIsBetter bool             `json:"higherIsBetter"`
	Rows           []globalBoardRow `json:"rows"`
}

// rankRows sorts a [accountID]value map into a ranked, full-roster board: every memberID gets a row (value 0 if
// absent from the map), ordered by value (higherIsBetter respected) with a stable accountID tie-break, and 1..N
// ranks assigned. The returned rows are in rank order; rank-1's accountID + value is also returned so the caller
// can decide whether to award a title.
func rankRows(memberIDs []string, names map[string]string, values map[string]int64, higherIsBetter bool) (rows []boardRow, topID string, topVal int64) {
	rows = make([]boardRow, 0, len(memberIDs))
	for _, aid := range memberIDs {
		rows = append(rows, boardRow{AccountID: aid, DisplayName: names[aid], Value: values[aid]})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Value != rows[j].Value {
			if higherIsBetter {
				return rows[i].Value > rows[j].Value
			}
			return rows[i].Value < rows[j].Value
		}
		return rows[i].AccountID < rows[j].AccountID // stable, deterministic tie-break
	})
	for i := range rows {
		rows[i].Rank = i + 1
	}
	if len(rows) > 0 {
		topID, topVal = rows[0].AccountID, rows[0].Value
	}
	return rows, topID, topVal
}

// GET /leaderboards?league=ID — the full per-league board set. Member-only (same posture as /leagues/members).
// Every board lists EVERY member (value 0 with no data) so the client can render full standings; "titles" awards
// fun badges to the meaningful rank-1 holders plus a "Bankrupt" tag to anyone currently in austerity.
func (s *Server) handleLeaderboards(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	leagueID := r.URL.Query().Get("league")
	if leagueID == "" {
		writeErr(w, http.StatusBadRequest, "missing league")
		return
	}
	if !s.store.IsMember(accountID, leagueID) {
		writeErr(w, http.StatusForbidden, "not a member of that league")
		return
	}
	memberIDs, err := s.store.LeagueMembers(leagueID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such league")
		return
	}

	// Per-league aggregates fetched ONCE and reused across boards (AuditLeague/InvestmentHistory/MarketMover are
	// O(events|reports); calling them per-member would be quadratic).
	auditNet, _, _ := s.store.AuditLeague(leagueID)    // net cash per member (nil-safe map read below)
	mover, _ := s.store.MarketMoverByAccount(leagueID) // |netSupply| sum per member
	patron := map[string]int64{}                       // § invested per member (payer side)
	for _, ev := range s.store.InvestmentHistory(leagueID) {
		patron[ev.PayerID] += ev.Cents
	}
	masterBuilder := s.store.CompletedProjectCountsByBuilder(leagueID)

	names := map[string]string{}
	// Per-member computed values for each board.
	netWorth := map[string]int64{}
	marketMover := map[string]int64{}
	tradeVolume := map[string]int64{}
	reliability := map[string]int64{}
	deadbeat := map[string]int64{}
	phoenix := map[string]int64{}
	population := map[string]int64{}
	happiness := map[string]int64{}
	austerityOf := map[string]bool{}
	hasHistory := map[string]bool{} // reliability is meaningful only with some trade/bond history

	for _, aid := range memberIDs {
		if acc, err := s.store.GetAccount(aid); err == nil {
			names[aid] = acc.DisplayName
			reliability[aid] = int64(acc.Reliability())
			deadbeat[aid] = int64(acc.MissedCount)
			hasHistory[aid] = acc.OnTimeCount+acc.MissedCount > 0
		} else {
			reliability[aid] = 100 // no account → default reliability, matches the roster handler
		}
		if auditNet != nil {
			netWorth[aid] = auditNet[aid]
		}
		marketMover[aid] = int64(math.Round(mover[aid]))

		// Completed-trade count.
		if trades, err := s.store.TradesFor(leagueID, aid); err == nil {
			var done int64
			for _, t := range trades {
				if t.Status == store.TradeCompleted {
					done++
				}
			}
			tradeVolume[aid] = done
		}

		austerity, _, _ := s.store.CityState(leagueID, aid)
		austerityOf[aid] = austerity

		// Phoenix: a comeback — escaped a defaulted debt (a bond as DEBTOR now cleared/written-off) AND not
		// currently in austerity.
		if !austerity {
			if bonds, err := s.store.BondsFor(leagueID, aid); err == nil {
				var escaped int64
				for _, b := range bonds {
					if b.DebtorID != aid {
						continue
					}
					if b.Status == store.BondCleared || b.Status == store.BondWrittenOff {
						escaped++
					}
				}
				phoenix[aid] = escaped
			}
		}

		if prof, ok := s.store.CityProfileOf(aid); ok && !prof.Suspect {
			// Suspect profiles (implausible client-reported deltas) are treated as value 0 on the client-reported
			// boards only, so spoofed stats can't top Population/Happiness or win the Metropolis title. The
			// server-authoritative boards (netWorth/reliability/tradeVolume/…) are unaffected.
			population[aid] = int64(prof.Population)
			happiness[aid] = int64(prof.Happiness)
		}
	}

	// Build each board in the order the client expects. rank-1 holders feed the title awards.
	type spec struct {
		id, label string
		values    map[string]int64
		higher    bool
	}
	specs := []spec{
		{"netWorth", "Net Worth", netWorth, true},
		{"marketMover", "Market Mover", marketMover, true},
		{"tradeVolume", "Trade Volume", tradeVolume, true},
		{"patron", "Patron", patron, true},
		{"masterBuilder", "Master Builder", masterBuilder, true},
		{"reliability", "Reliability", reliability, true},
		{"deadbeat", "Deadbeat", deadbeat, true}, // more misses = top of the shame board
		{"phoenix", "Phoenix", phoenix, true},
		{"population", "Population", population, true},
		{"happiness", "Happiness", happiness, true},
	}

	boards := make([]boardDTO, 0, len(specs))
	titles := map[string][]string{}
	award := func(aid, title string) {
		if aid == "" {
			return
		}
		titles[aid] = append(titles[aid], title)
	}
	for _, sp := range specs {
		rows, topID, topVal := rankRows(memberIDs, names, sp.values, sp.higher)
		boards = append(boards, boardDTO{ID: sp.id, Label: sp.label, HigherIsBetter: sp.higher, Rows: rows})

		switch sp.id {
		case "netWorth":
			if topVal > 0 {
				award(topID, "Market Baron")
			}
		case "marketMover":
			if topVal > 0 {
				award(topID, "Market Mover")
			}
		case "tradeVolume":
			if topVal > 0 {
				award(topID, "Top Trader")
			}
		case "patron":
			if topVal > 0 {
				award(topID, "Patron")
			}
		case "masterBuilder":
			if topVal > 0 {
				award(topID, "Master Builder")
			}
		case "reliability":
			if topVal > 0 && hasHistory[topID] {
				award(topID, "Good Credit")
			}
		case "deadbeat":
			if topVal > 0 { // MissedCount > 0
				award(topID, "Deadbeat")
			}
		case "phoenix":
			if topVal > 0 {
				award(topID, "Phoenix")
			}
		case "population":
			if topVal > 0 {
				award(topID, "Metropolis")
			}
		}
	}
	// Anyone currently in austerity is publicly "Bankrupt" (independent of any board rank).
	for _, aid := range memberIDs {
		if austerityOf[aid] {
			award(aid, "Bankrupt")
		}
	}

	// Emit titles as a sorted array (deterministic; client-parseable — see titleEntry).
	titleList := make([]titleEntry, 0, len(titles))
	for aid, ts := range titles {
		titleList = append(titleList, titleEntry{AccountID: aid, Titles: ts})
	}
	sort.Slice(titleList, func(i, j int) bool { return titleList[i].AccountID < titleList[j].AccountID })

	writeJSON(w, http.StatusOK, map[string]any{
		"leagueId": leagueID,
		"boards":   boards,
		"titles":   titleList,
	})
}

// percentileTier returns the percentile (0..100, higher rank → higher percentile) for rank r of n, and its tier
// band. With a single entry the lone player is the top (100th percentile / Diamond).
func percentileTier(rank, n int) (int, string) {
	pct := 100
	if n > 1 {
		// rank 1 → ~100, rank n → ~0. (n-rank)/(n-1) * 100.
		pct = int(math.Round(float64(n-rank) / float64(n-1) * 100))
	}
	return pct, tierFor(pct)
}

func tierFor(pct int) string {
	switch {
	case pct < 40:
		return "Bronze"
	case pct < 70:
		return "Silver"
	case pct < 90:
		return "Gold"
	case pct < 98:
		return "Platinum"
	default:
		return "Diamond"
	}
}

// anonName returns the privacy-preserving label for a GLOBAL board row: a stable per-account pseudonym derived
// from the id ("Mayor-xxxxxx"), NEVER the player's real chosen display name. /global-leaderboards is visible to
// any authenticated stranger (no shared league), so surfacing real handles there would de-anonymize the
// cross-league board and pair a real name with exact financials. The caller finds their own row via the "you"
// flag instead.
func anonName(id string) string {
	if len(id) >= 6 {
		return "Mayor-" + id[:6]
	}
	return "Mayor-" + id
}

// GET /global-leaderboards — cross-league, anonymized standings on SERVER-AUTHORITATIVE metrics only (no
// population/happiness, which are client-reported and spoofable). Any authenticated account; no league param.
// Each board returns the top 100 plus the caller's own row (flagged "you") even if outside the top 100, with a
// percentile + tier per row. Read-only.
func (s *Server) handleGlobalLeaderboards(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	ids := s.store.AllAccountIDs()

	// Per-league aggregates are fetched once per league and reused for every member of that league.
	netByLeague := map[string]map[string]int64{} // leagueID -> AuditLeague net map
	volByLeague := map[string]map[string]int64{} // leagueID -> TradeVolumeByLeague (completed-trade count per member)

	globalNetWorth := map[string]int64{}
	globalReliability := map[string]int64{}
	globalTradeVolume := map[string]int64{}

	// Candidate set = accounts that are in at least one league (a player in no league has no standings and would
	// otherwise pad the boards with phantom value-0 / reliability-100 "Diamond" rows and inflate the percentile
	// denominator). The caller is always included so they can see their own row even before joining a league.
	cand := make([]string, 0, len(ids))
	for _, aid := range ids {
		leagues, err := s.store.LeaguesForAccount(aid)
		if err != nil || len(leagues) == 0 {
			if aid != accountID {
				continue
			}
			leagues = nil
		}
		cand = append(cand, aid)
		if acc, err := s.store.GetAccount(aid); err == nil {
			globalReliability[aid] = int64(acc.Reliability())
		} else {
			globalReliability[aid] = 100
		}
		for _, lg := range leagues {
			net, ok := netByLeague[lg.ID]
			if !ok {
				net, _, _ = s.store.AuditLeague(lg.ID)
				netByLeague[lg.ID] = net // cache (may be nil; cached either way)
			}
			if net != nil {
				globalNetWorth[aid] += net[aid]
			}
			vol, ok := volByLeague[lg.ID]
			if !ok {
				vol = s.store.TradeVolumeByLeague(lg.ID)
				volByLeague[lg.ID] = vol // fetch once per league, reuse for every member
			}
			globalTradeVolume[aid] += vol[aid]
		}
	}

	type gspec struct {
		id, label string
		values    map[string]int64
	}
	specs := []gspec{
		{"globalNetWorth", "Global Net Worth", globalNetWorth},
		{"globalReliability", "Global Reliability", globalReliability},
		{"globalTradeVolume", "Global Trade Volume", globalTradeVolume},
	}

	boards := make([]globalBoardDTO, 0, len(specs))
	for _, sp := range specs {
		// Sort all candidates by value desc, stable accountID tie-break.
		sorted := make([]string, len(cand))
		copy(sorted, cand)
		sort.SliceStable(sorted, func(i, j int) bool {
			vi, vj := sp.values[sorted[i]], sp.values[sorted[j]]
			if vi != vj {
				return vi > vj
			}
			return sorted[i] < sorted[j]
		})
		n := len(sorted)
		rankOf := make(map[string]int, n)
		for i, aid := range sorted {
			rankOf[aid] = i + 1
		}

		rows := make([]globalBoardRow, 0, 101)
		callerIncluded := false
		for i, aid := range sorted {
			if i >= 100 && aid != accountID {
				continue // top 100 only, but always keep the caller's row
			}
			rank := i + 1
			pct, tier := percentileTier(rank, n)
			you := aid == accountID
			if you {
				callerIncluded = true
			}
			rows = append(rows, globalBoardRow{
				Rank: rank, DisplayName: anonName(aid), Value: sp.values[aid],
				Percentile: pct, Tier: tier, You: you,
			})
		}
		// If the caller fell outside the top 100, append their own row explicitly (already handled above by the
		// aid==accountID exception, but guard for the case the caller isn't in the candidate set at all).
		if !callerIncluded {
			rank := rankOf[accountID]
			if rank == 0 { // caller had no account row (shouldn't happen post-auth) → represent as last
				rank = n + 1
			}
			pct, tier := percentileTier(rank, n)
			rows = append(rows, globalBoardRow{
				Rank: rank, DisplayName: anonName(accountID), Value: sp.values[accountID],
				Percentile: pct, Tier: tier, You: true,
			})
		}

		boards = append(boards, globalBoardDTO{ID: sp.id, Label: sp.label, HigherIsBetter: true, Rows: rows})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"boards": boards,
	})
}
