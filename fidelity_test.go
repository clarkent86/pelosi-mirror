package main

import (
	"math"
	"testing"
)

func TestParsePositions(t *testing.T) {
	rows := []map[string]string{
		{"sym": "Account: IndividualZ12345678 Manage dividends"},
		{"sym": "Cash HELD IN MONEY MARKET", "curVal": "$500.00"},
		{"sym": "AAPL APPLE INC", "curVal": "$90.00", "qty": "0.256"},
		{"sym": "MMM 3M CO", "curVal": "$1,234.50", "qty": "20"},
		{"sym": "Account total", "curVal": "$2,000.00"},
		{"sym": ""},
		{"sym": "Account: Roth IRA987654321 Manage dividends"},
		{"sym": "Cash HELD IN MONEY MARKET", "curVal": "$900.00"},
		{"sym": "FXAIX FIDELITY 500 INDEX FUND", "curVal": "$4,000.00", "qty": "19.1"},
	}
	a, err := parsePositions(rows, "Z12345678")
	if err != nil {
		t.Fatal(err)
	}
	if a.Cash != 500 || len(a.Positions) != 2 || a.Positions[1].Symbol != "MMM" || math.Abs(a.Positions[1].Value-1234.50) > 0.001 || a.Positions[0].Qty != 0.256 {
		t.Fatalf("%+v", a)
	}
	if _, err := parsePositions(rows, "X999"); err == nil {
		t.Fatal("missing account should be an error")
	}
}

func TestCheckPreview(t *testing.T) {
	buy := Order{Symbol: "BE", Side: "buy", Dollars: 10}
	ok := ticketResult{Account: "Individual (Z12345678)", Symbol: "BE", Action: "Buy", Quantity: "$10.00", OrderType: "Market"}
	if err := checkPreview(ok, buy, "Z12345678"); err != nil {
		t.Fatal(err)
	}
	bad := []ticketResult{ok, ok, ok, ok, ok}
	bad[0].Account = "Roth IRA (987654321)"
	bad[1].Symbol = "BEN"
	bad[2].Action = "Sell"
	bad[3].Quantity = "$100.00"
	bad[4].Quantity = "10" // shares, not dollars
	for i, r := range bad {
		if checkPreview(r, buy, "Z12345678") == nil {
			t.Errorf("case %d should fail", i)
		}
	}
	sell := Order{Symbol: "PANW", Side: "sell", Shares: 0.102}
	if err := checkPreview(ticketResult{Account: "Individual (Z12345678)", Symbol: "PANW", Action: "Sell", Quantity: "0.102", OrderType: "Market"}, sell, "Z12345678"); err != nil {
		t.Fatal(err)
	}
}
