package market

import (
	"math"
	"testing"
)

var testParams = Params{VolumeRef: 20000, Min: 0.5, Max: 2.0}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCommodityIndices_NetSupplyLowersPrice(t *testing.T) {
	// A net exporter floods the market → index below 1.0. Half the VolumeRef → 1 - 0.5 = 0.5.
	got := CommodityIndices([]Report{{Commodity: "Oil", NetSupply: 10000}}, testParams)
	if !approx(got["Oil"], 0.5) {
		t.Fatalf("Oil index = %v, want 0.5", got["Oil"])
	}
}

func TestCommodityIndices_NetDemandRaisesPrice(t *testing.T) {
	// A net importer drains the market → index above 1.0. -VolumeRef → 1 - (-1) = 2.0.
	got := CommodityIndices([]Report{{Commodity: "Grain", NetSupply: -20000}}, testParams)
	if !approx(got["Grain"], 2.0) {
		t.Fatalf("Grain index = %v, want 2.0", got["Grain"])
	}
}

func TestCommodityIndices_SumsAcrossMembers(t *testing.T) {
	// Friend group: collective net supply is what moves the shared price.
	reports := []Report{
		{Commodity: "Ore", NetSupply: 4000},
		{Commodity: "Ore", NetSupply: 6000},
		{Commodity: "Coal", NetSupply: -2000},
	}
	got := CommodityIndices(reports, testParams)
	if !approx(got["Ore"], 1.0-10000.0/20000.0) { // 0.5
		t.Errorf("Ore index = %v, want 0.5", got["Ore"])
	}
	if !approx(got["Coal"], 1.0-(-2000.0)/20000.0) { // 1.1
		t.Errorf("Coal index = %v, want 1.1", got["Coal"])
	}
}

func TestCommodityIndicesWithShields_DampensMatchingAccountCommodity(t *testing.T) {
	reports := []Report{
		{AccountID: "A", Commodity: "Oil", NetSupply: 10000},
		{AccountID: "B", Commodity: "Oil", NetSupply: 10000},
	}
	shields := []Shield{{AccountID: "A", Commodity: "Oil", DampeningBips: 3000}}
	got := CommodityIndicesWithShields(reports, testParams, shields)
	// A counts 70% (7000) and B counts fully (10000): net 17000 -> 1 - 17000/20000 = 0.15, clamped to 0.5.
	if !approx(got["Oil"], 0.5) {
		t.Fatalf("Oil index = %v, want clamp floor 0.5", got["Oil"])
	}

	got = CommodityIndicesWithShields(
		[]Report{{AccountID: "A", Commodity: "Oil", NetSupply: 10000}},
		testParams,
		shields,
	)
	if !approx(got["Oil"], 1.0-7000.0/20000.0) {
		t.Fatalf("shielded Oil index = %v, want 0.65", got["Oil"])
	}
}

func TestCommodityIndices_ClampsToBounds(t *testing.T) {
	got := CommodityIndices([]Report{
		{Commodity: "Flood", NetSupply: 1e9},    // huge supply → floored
		{Commodity: "Drought", NetSupply: -1e9}, // huge demand → ceiled
	}, testParams)
	if got["Flood"] != testParams.Min {
		t.Errorf("Flood index = %v, want floor %v", got["Flood"], testParams.Min)
	}
	if got["Drought"] != testParams.Max {
		t.Errorf("Drought index = %v, want ceil %v", got["Drought"], testParams.Max)
	}
}

func TestCommodityIndices_IgnoresBlankCommodity(t *testing.T) {
	got := CommodityIndices([]Report{{Commodity: "", NetSupply: 5000}}, testParams)
	if len(got) != 0 {
		t.Fatalf("blank commodity should be ignored, got %v", got)
	}
}

func TestMarketIndex_EmptyIsNeutral(t *testing.T) {
	if got := MarketIndex(nil, testParams); got != 1.0 {
		t.Fatalf("empty market index = %v, want neutral 1.0", got)
	}
}

func TestMarketIndex_MeanOfCommodities(t *testing.T) {
	// Oil → 0.5, Grain → 1.5; mean = 1.0.
	reports := []Report{
		{Commodity: "Oil", NetSupply: 10000},
		{Commodity: "Grain", NetSupply: -10000},
	}
	if got := MarketIndex(reports, testParams); !approx(got, 1.0) {
		t.Fatalf("market index = %v, want 1.0", got)
	}
}

func TestParamsSane_GuardsZeroValue(t *testing.T) {
	// A zero Params must not divide by zero or invert the clamp.
	got := CommodityIndices([]Report{{Commodity: "X", NetSupply: 1000}}, Params{})
	if math.IsNaN(got["X"]) || math.IsInf(got["X"], 0) {
		t.Fatalf("zero Params produced %v", got["X"])
	}
	if got["X"] < 0.5 || got["X"] > 2.0 {
		t.Fatalf("zero Params index %v outside default bounds", got["X"])
	}
}
