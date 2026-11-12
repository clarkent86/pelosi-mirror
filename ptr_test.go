package main

import (
	"os"
	"testing"
)

func TestParsePTR(t *testing.T) {
	for _, id := range []string{"20033725", "20034836", "20035143", "20035553", "20035780"} {
		b, err := os.ReadFile("testdata/" + id + ".pdf")
		if err != nil {
			t.Fatal(err)
		}
		txns, err := ParsePTR(b)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("== %s: %d txns", id, len(txns))
		for _, x := range txns {
			t.Logf("%-2s %-6s %-2s %-11s %s %d-%d | %.60s | %.90s", x.Owner, x.Ticker, x.AssetType, x.Type, x.Date, x.Low, x.High, x.Asset, x.Desc)
		}
	}
}
