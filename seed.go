package main

// The starting model for a new install (when there is no state file yet).
//
// seed.json ships with an estimate of Pelosi's portfolio, kept current through the
// filings it lists; the app folds in anything newer on its first check. SEED_FILE can
// point at a different seed with the same shape.
//
// INITIAL_CASH is how much of the Fidelity account's cash the strategy may use. The
// first proposal invests it across the model's weights. Any positions you already
// hold in the model's tickers count as part of the strategy.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

//go:embed seed.json
var defaultSeed []byte

type seedFile struct {
	AsOf    string             `json:"as_of"`
	Applied []string           `json:"applied_filings"`
	Model   map[string]float64 `json:"model"`
}

func seedState() State {
	b := defaultSeed
	if p := os.Getenv("SEED_FILE"); p != "" {
		var err error
		if b, err = os.ReadFile(p); err != nil {
			panic(fmt.Sprintf("SEED_FILE: %v", err))
		}
	}
	var sf seedFile
	if err := json.Unmarshal(b, &sf); err != nil {
		panic(fmt.Sprintf("seed: %v", err))
	}
	cash, _ := strconv.ParseFloat(os.Getenv("INITIAL_CASH"), 64)
	s := State{Model: Model{}, Applied: map[string]bool{}, NextID: 1, Managed: map[string]bool{}, SleeveCash: cash}
	for k, v := range sf.Model {
		s.Model[k] = v
		s.Managed[k] = true
	}
	for _, id := range sf.Applied {
		s.Applied[id] = true
	}
	return s
}
