package main

// The portfolio model and rebalance planning.
//
// The model is an estimate of the member's holdings in dollars, built from disclosed
// trades: a purchase adds the midpoint of its range, a full sale of stock zeroes the
// position, and a partial sale (or an options sale) subtracts the midpoint. Call
// options count as the underlying stock. Exercising an option is not a new purchase,
// because the option already counted. Only stocks, options and ETFs with a ticker are
// mirrored; LLC interests, partnership units ([AB]) and the like are not.
//
// The starting model (seed.json) began as Autopilot's "Pelosi Tracker+" weights scaled
// to $50M, so a new trade moves the weights by its size relative to that.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type Model map[string]float64 // ticker -> estimated dollars

var mirrored = map[string]bool{"ST": true, "OP": true, "EF": true}

// Apply folds one disclosed transaction into the model and says what it did.
func (m Model) Apply(t Txn) (bool, string) {
	switch {
	case t.Ticker == "":
		return false, "no ticker"
	case !mirrored[t.AssetType]:
		return false, "asset type " + t.AssetType + " is not mirrored"
	case t.Type == "E":
		return false, "exchange"
	case t.Type == "P" && strings.Contains(strings.ToLower(t.Desc), "exercised"):
		return false, "option exercise (already counted when the option was bought)"
	}
	before := m[t.Ticker]
	if t.Type != "P" && before == 0 {
		return false, "sale of " + t.Ticker + ", which the model doesn't hold"
	}
	switch {
	case t.Type == "P":
		m[t.Ticker] += t.Mid()
	case t.Type == "S" && t.AssetType != "OP":
		m[t.Ticker] = 0
	default: // partial sale, or sale of options
		m[t.Ticker] = math.Max(0, m[t.Ticker]-t.Mid())
	}
	if m[t.Ticker] == 0 {
		delete(m, t.Ticker)
	}
	return true, fmt.Sprintf("%s $%s -> $%s", t.Ticker, human(before), human(m[t.Ticker]))
}

// Weights returns each ticker's share of the model.
func (m Model) Weights() map[string]float64 {
	var sum float64
	for _, v := range m {
		sum += v
	}
	w := map[string]float64{}
	for k, v := range m {
		if sum > 0 && v > 0 {
			w[k] = v / sum
		}
	}
	return w
}

func human(v float64) string {
	switch {
	case v >= 1e6:
		return fmt.Sprintf("%.2fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.0fK", v/1e3)
	}
	return fmt.Sprintf("%.0f", v)
}

// Position is one holding in the brokerage account.
type Position struct {
	Symbol string  `json:"symbol"`
	Qty    float64 `json:"qty"`
	Value  float64 `json:"value"`
}

// Account is a snapshot of the brokerage account.
type Account struct {
	Positions []Position `json:"positions"`
	Cash      float64    `json:"cash"` // core position (money market), available to trade
	AsOf      string     `json:"as_of"`
}

func (a Account) Total() float64 {
	t := a.Cash
	for _, p := range a.Positions {
		t += p.Value
	}
	return t
}

// Order is one proposed trade. Buys are in dollars (fractional); sells are in shares.
type Order struct {
	Symbol  string  `json:"symbol"`
	Side    string  `json:"side"` // buy, sell
	Dollars float64 `json:"dollars,omitempty"`
	Shares  float64 `json:"shares,omitempty"`
	All     bool    `json:"all,omitempty"` // sell the whole position
	From    float64 `json:"from"`          // position value now
	To      float64 `json:"to"`            // target value
	Status  string  `json:"status,omitempty"`
	Result  string  `json:"result,omitempty"`
}

func (o Order) String() string {
	if o.Side == "buy" {
		return fmt.Sprintf("Buy $%.2f %s", o.Dollars, o.Symbol)
	}
	if o.All {
		return fmt.Sprintf("Sell all %s (%.3f sh)", o.Symbol, o.Shares)
	}
	return fmt.Sprintf("Sell %.3f sh %s (~$%.2f)", o.Shares, o.Symbol, o.Dollars)
}

type PlanOpts struct {
	MinTrade    float64 // skip changes smaller than this, in dollars
	CashReserve float64 // leave this much cash uninvested
}

// Plan works out the orders that bring the account to the target weights.
// Sells come first so their proceeds fund the buys.
func Plan(w map[string]float64, acct Account, o PlanOpts) []Order {
	investable := acct.Total() - o.CashReserve
	held := map[string]Position{}
	syms := map[string]bool{}
	for _, p := range acct.Positions {
		held[p.Symbol] = p
		syms[p.Symbol] = true
	}
	for s := range w {
		syms[s] = true
	}
	var sells, buys []Order
	for s := range syms {
		p, target := held[s], w[s]*investable
		diff := target - p.Value
		switch {
		case w[s] == 0 && p.Qty > 0:
			sells = append(sells, Order{Symbol: s, Side: "sell", Shares: p.Qty, All: true, Dollars: p.Value, From: p.Value})
		case diff <= -o.MinTrade && p.Qty > 0:
			price := p.Value / p.Qty
			sh := math.Floor(-diff/price*1000) / 1000
			if sh <= 0 {
				continue
			}
			if sh >= p.Qty*0.99 {
				sells = append(sells, Order{Symbol: s, Side: "sell", Shares: p.Qty, All: true, Dollars: p.Value, From: p.Value, To: target})
			} else {
				sells = append(sells, Order{Symbol: s, Side: "sell", Shares: sh, Dollars: sh * price, From: p.Value, To: target})
			}
		case diff >= o.MinTrade:
			buys = append(buys, Order{Symbol: s, Side: "buy", Dollars: diff, From: p.Value, To: target})
		}
	}
	budget := acct.Cash - o.CashReserve
	for _, s := range sells {
		budget += s.Dollars
	}
	var want float64
	for _, b := range buys {
		want += b.Dollars
	}
	scale := 1.0
	if want > budget && want > 0 {
		scale = math.Max(0, budget) / want
	}
	var out []Order
	sort.Slice(sells, func(i, j int) bool { return sells[i].Dollars > sells[j].Dollars })
	sort.Slice(buys, func(i, j int) bool { return buys[i].Dollars > buys[j].Dollars })
	out = append(out, sells...)
	for _, b := range buys {
		b.Dollars = math.Floor(b.Dollars*scale*100) / 100
		if b.Dollars >= math.Max(1, o.MinTrade*scale) { // Fidelity's fractional minimum is $1
			out = append(out, b)
		}
	}
	return out
}
