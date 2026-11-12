package main

import (
	"math"
	"testing"
)

func TestApply(t *testing.T) {
	m := Model{"INTC": 1000, "AAPL": 500}
	cases := []struct {
		t    Txn
		ok   bool
		want float64
		sym  string
	}{
		{Txn{Ticker: "BE", AssetType: "ST", Type: "P", Low: 1000001, High: 5000000}, true, 3000000.5, "BE"},
		{Txn{Ticker: "BE", AssetType: "OP", Type: "P", Low: 500001, High: 1000000}, true, 3750001, "BE"},
		{Txn{Ticker: "INTC", AssetType: "ST", Type: "P", Low: 1, High: 1, Desc: "Exercised 50 call options"}, false, 1000, "INTC"},
		{Txn{Ticker: "", AssetType: "AB", Type: "P", Low: 1, High: 1}, false, 0, ""},
		{Txn{Ticker: "AB", AssetType: "AB", Type: "P", Low: 1, High: 1}, false, 0, "AB"},
		{Txn{Ticker: "AAPL", AssetType: "ST", Type: "S (partial)", Low: 100, High: 200}, true, 350, "AAPL"},
		{Txn{Ticker: "INTC", AssetType: "ST", Type: "S", Low: 1, High: 2}, true, 0, "INTC"},
		{Txn{Ticker: "WBD", AssetType: "ST", Type: "S", Low: 50001, High: 100000}, false, 0, "WBD"},
	}
	for i, c := range cases {
		ok, note := m.Apply(c.t)
		if ok != c.ok || (c.sym != "" && math.Abs(m[c.sym]-c.want) > 0.01) {
			t.Errorf("case %d: ok=%v %s, %s=%v want %v %v", i, ok, note, c.sym, m[c.sym], c.ok, c.want)
		}
	}
	if _, ok := m["INTC"]; ok {
		t.Error("zeroed position should be removed")
	}
}

func TestPlan(t *testing.T) {
	w := map[string]float64{"AAA": 0.5, "BBB": 0.5}
	acct := Account{Cash: 5, Positions: []Position{
		{Symbol: "AAA", Qty: 10, Value: 100}, // $10/sh; total 195, investable 190, target 95
		{Symbol: "CCC", Qty: 2, Value: 50},   // not in model: sell all
		{Symbol: "BBB", Qty: 1, Value: 40},
	}}
	orders := Plan(w, acct, PlanOpts{MinTrade: 2, CashReserve: 5})
	got := map[string]Order{}
	for _, o := range orders {
		got[o.Symbol] = o
	}
	if o := got["CCC"]; o.Side != "sell" || !o.All || o.Shares != 2 {
		t.Errorf("CCC: %+v", o)
	}
	if o := got["AAA"]; o.Side != "sell" || math.Abs(o.Shares-0.5) > 0.001 {
		t.Errorf("AAA: %+v", o)
	}
	if o := got["BBB"]; o.Side != "buy" || math.Abs(o.Dollars-55) > 0.02 {
		t.Errorf("BBB: %+v", o)
	}
	if orders[0].Side != "sell" || orders[len(orders)-1].Side != "buy" {
		t.Error("sells should come before buys")
	}
	// Buys never spend more than cash + sale proceeds - reserve.
	var spend, proceeds float64
	for _, o := range orders {
		if o.Side == "buy" {
			spend += o.Dollars
		} else {
			proceeds += o.Dollars
		}
	}
	if spend > acct.Cash+proceeds-5+0.01 {
		t.Errorf("overspend: %v > %v", spend, acct.Cash+proceeds-5)
	}
}
