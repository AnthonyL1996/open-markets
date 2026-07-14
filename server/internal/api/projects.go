package api

import (
	"errors"
	"net/http"
	"sort"

	"openmarkets/server/internal/store"
)

// Co-op MEGAPROJECTS (Great Works, social slice 4): member-only endpoints to view a league's projects and
// contribute commodities + § toward them. A completed project grants every builder a lasting buff (an Effect that
// rides the existing /citystate path), and a § contribution is the only money-moving action here — booked
// member → "project:"+id (conserving), so it cannot break AuditLeague.
//
// WIRE SHAPE: the client's OmJson reader CANNOT bind dynamic-key JSON maps, so the Project's Goods and By maps are
// emitted as ARRAYS of {commodity,qty} / {accountId,score} pairs (sorted for a stable order).

// projectReqDTO is one commodity requirement on the wire.
type projectReqDTO struct {
	Commodity string `json:"commodity"`
	Qty       int64  `json:"qty"`
}

// goodsPairDTO is one commodity → units-contributed pair (Goods map flattened to an array for OmJson).
type goodsPairDTO struct {
	Commodity string `json:"commodity"`
	Qty       int64  `json:"qty"`
}

// builderPairDTO is one accountId → builder-score pair (By map flattened to an array for OmJson).
type builderPairDTO struct {
	AccountID string `json:"accountId"`
	Score     int64  `json:"score"`
}

// projectDTO is one project on the wire — Goods/By emitted as ARRAYS (OmJson can't bind maps).
type projectDTO struct {
	ID                   string           `json:"id"`
	Name                 string           `json:"name"`
	Description          string           `json:"description"`
	Reqs                 []projectReqDTO  `json:"reqs"`
	GoldReqCents         int64            `json:"goldReqCents"`
	Goods                []goodsPairDTO   `json:"goods"`
	Gold                 int64            `json:"gold"`
	By                   []builderPairDTO `json:"by"`
	BuffKind             string           `json:"buffKind"`
	BuffMagnitudeCents   int64            `json:"buffMagnitudeCents"`
	BuffDays             int              `json:"buffDays"`
	TradeRewardKind      string           `json:"tradeRewardKind,omitempty"`
	TradeRewardCommodity string           `json:"tradeRewardCommodity,omitempty"`
	TradeRewardPctBips   int              `json:"tradeRewardPctBips,omitempty"`
	Status               string           `json:"status"`
}

// projectsDTO is the GET /projects response.
type projectsDTO struct {
	LeagueID string       `json:"leagueId"`
	Projects []projectDTO `json:"projects"`
}

// toProjectDTO flattens a store.Project to its wire shape, emitting Goods/By as sorted arrays.
func toProjectDTO(p store.Project) projectDTO {
	reqs := make([]projectReqDTO, 0, len(p.Reqs))
	for _, r := range p.Reqs {
		reqs = append(reqs, projectReqDTO{Commodity: r.Commodity, Qty: r.Qty})
	}
	goods := make([]goodsPairDTO, 0, len(p.Goods))
	for c, q := range p.Goods {
		goods = append(goods, goodsPairDTO{Commodity: c, Qty: q})
	}
	sort.Slice(goods, func(i, j int) bool { return goods[i].Commodity < goods[j].Commodity })
	by := make([]builderPairDTO, 0, len(p.By))
	for a, s := range p.By {
		by = append(by, builderPairDTO{AccountID: a, Score: s})
	}
	sort.Slice(by, func(i, j int) bool { return by[i].AccountID < by[j].AccountID })
	return projectDTO{
		ID: p.ID, Name: p.Name, Description: p.Description, Reqs: reqs, GoldReqCents: p.GoldReqCents,
		Goods: goods, Gold: p.Gold, By: by, BuffKind: p.BuffKind, BuffMagnitudeCents: p.BuffMagnitudeCents,
		BuffDays: p.BuffDays, TradeRewardKind: p.TradeRewardKind, TradeRewardCommodity: p.TradeRewardCommodity,
		TradeRewardPctBips: p.TradeRewardPctBips, Status: p.Status,
	}
}

// handleProjects serves a league's Great Works (open + completed), member-only (like /feed).
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	_, leagueID, ok := s.authMemberFeed(w, r)
	if !ok {
		return
	}
	projs := s.store.ProjectsFor(leagueID)
	out := make([]projectDTO, 0, len(projs))
	for _, p := range projs {
		out = append(out, toProjectDTO(p))
	}
	writeJSON(w, http.StatusOK, projectsDTO{LeagueID: leagueID, Projects: out})
}

// contributeGoldBody is the POST /projects/{id}/contribute-gold request body.
type contributeGoldBody struct {
	Cents int64 `json:"cents"`
}

// contributeGoodsBody is the POST /projects/{id}/contribute-goods request body.
type contributeGoodsBody struct {
	Commodity string `json:"commodity"`
	Qty       int64  `json:"qty"`
}

// projectLeagueOf resolves the league a project belongs to, so the contribute handlers can member-gate the caller
// against the right league (the project id is in the path; the league comes from the project itself).
func (s *Server) projectLeagueOf(projectID string) (store.Project, bool) {
	p, err := s.store.GetProject(projectID)
	if err != nil {
		return store.Project{}, false
	}
	return p, true
}

// handleContributeProjectGold books a § contribution toward a project (member-only). The § moves caller →
// "project:"+id, conserving cash. Returns the updated project DTO.
func (s *Server) handleContributeProjectGold(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	projectID := r.PathValue("id")
	p, ok := s.projectLeagueOf(projectID)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such project")
		return
	}
	if !s.store.IsMember(accountID, p.LeagueID) {
		writeErr(w, http.StatusForbidden, "not a member of that league")
		return
	}
	var body contributeGoldBody
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	if body.Cents <= 0 {
		writeErr(w, http.StatusBadRequest, "cents must be > 0")
		return
	}
	updated, _, err := s.store.ContributeProjectGold(p.LeagueID, accountID, projectID, body.Cents)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such project")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "project is not accepting § contributions (closed or § requirement met)")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not contribute")
	default:
		writeJSON(w, http.StatusOK, toProjectDTO(updated))
	}
}

// handleContributeProjectGoods books a commodity contribution toward a project (member-only). NO money moves; the
// units are tracked counts (the client has already removed the physical stock from its [trade] depots). The qty is
// capped server-side at the commodity's remaining requirement. Returns the updated project DTO.
func (s *Server) handleContributeProjectGoods(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	projectID := r.PathValue("id")
	p, ok := s.projectLeagueOf(projectID)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such project")
		return
	}
	if !s.store.IsMember(accountID, p.LeagueID) {
		writeErr(w, http.StatusForbidden, "not a member of that league")
		return
	}
	var body contributeGoodsBody
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	if body.Commodity == "" || body.Qty <= 0 {
		writeErr(w, http.StatusBadRequest, "commodity and qty>0 required")
		return
	}
	updated, credited, err := s.store.ContributeProjectGoods(p.LeagueID, accountID, projectID, body.Commodity, body.Qty)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such project")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "project is not accepting this commodity (closed, not required, or already met)")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not contribute")
	default:
		writeJSON(w, http.StatusOK, contributeGoodsDTO{projectDTO: toProjectDTO(updated), Credited: credited})
	}
}

// contributeGoodsDTO is the POST /projects/{id}/contribute-goods response: the updated project plus credited —
// the units actually applied this call (capped to the remaining requirement), so the client can refund the rest.
type contributeGoodsDTO struct {
	projectDTO
	Credited int64 `json:"credited"`
}
