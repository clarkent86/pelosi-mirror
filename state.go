package main

import (
	"encoding/json"
	"math"
	"os"
	"sync"
	"time"
)

// TxnNote is a disclosed transaction and what the model did with it.
type TxnNote struct {
	Txn
	Applied bool   `json:"applied"`
	Note    string `json:"note"`
}

// Proposal is a set of orders waiting for (or past) approval.
type Proposal struct {
	ID      int                `json:"id"`
	Created time.Time          `json:"created"`
	Reason  string             `json:"reason"`
	Orders  []Order            `json:"orders"`
	Weights map[string]float64 `json:"weights"`
	Total   float64            `json:"total"`
	Status  string             `json:"status"` // pending, approved, executing, done, failed, rejected, superseded
	Decided time.Time          `json:"decided,omitempty"`
	DryRun  bool               `json:"dry_run"`
	MsgID   string             `json:"msg_id,omitempty"`
	Log     []string           `json:"log,omitempty"`
}

type State struct {
	Model       Model           `json:"model"`
	Applied     map[string]bool `json:"applied_filings"`
	Txns        []TxnNote       `json:"txns"`
	Proposals   []*Proposal     `json:"proposals"`
	NextID      int             `json:"next_id"`
	Account     *Account        `json:"account,omitempty"`
	NeedPlan    string          `json:"need_plan,omitempty"` // why a rebalance is due ("" = none)
	LastPoll    time.Time       `json:"last_poll"`
	LastError   string          `json:"last_error,omitempty"`
	LoginNagged time.Time       `json:"login_nagged"`
	// The Pelosi "sleeve" shares a Fidelity account with other holdings. Only Managed
	// tickers (every ticker the model has held) and SleeveCash are ever traded.
	Managed    map[string]bool `json:"managed"`
	SleeveCash float64         `json:"sleeve_cash"`
	// Live turns real order placement on. Off (the default) stops orders at
	// Fidelity's preview. Switched on the web page, so no app restart is needed.
	Live bool `json:"live"`
}

// sleeveLocked is the part of the account that mirrors the model. Callers hold mu.
func (st *Store) sleeveLocked(a Account) Account {
	out := Account{Cash: math.Min(st.S.SleeveCash, a.Cash), AsOf: a.AsOf}
	for _, p := range a.Positions {
		if st.S.Managed[p.Symbol] || st.S.Model[p.Symbol] > 0 {
			out.Positions = append(out.Positions, p)
		}
	}
	return out
}

type Store struct {
	mu   sync.Mutex
	path string
	S    State
}

func LoadStore(path string) (*Store, error) {
	st := &Store{path: path}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		st.S = seedState()
		return st, st.saveLocked()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &st.S); err != nil {
		return nil, err
	}
	if st.S.Applied == nil {
		st.S.Applied = map[string]bool{}
	}
	if st.S.Model == nil {
		st.S.Model = Model{}
	}
	if st.S.Managed == nil { // state from before the sleeve existed
		seed := seedState()
		st.S.Managed, st.S.SleeveCash = seed.Managed, seed.SleeveCash
	}
	for k := range st.S.Model {
		st.S.Managed[k] = true
	}
	return st, nil
}

// saveLocked writes the state atomically. Callers hold mu.
func (st *Store) saveLocked() error {
	b, err := json.MarshalIndent(st.S, "", " ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}

func (st *Store) pending() *Proposal {
	for _, p := range st.S.Proposals {
		if p.Status == "pending" {
			return p
		}
	}
	return nil
}

func (st *Store) proposal(id int) *Proposal {
	for _, p := range st.S.Proposals {
		if p.ID == id {
			return p
		}
	}
	return nil
}
