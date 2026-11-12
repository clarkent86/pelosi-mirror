package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type fakeBroker struct{ placed []Order }

func (f *fakeBroker) Snapshot(context.Context) (Account, error) {
	return Account{Cash: 100, Positions: []Position{{Symbol: "PANW", Qty: 1, Value: 200}, {Symbol: "OLD", Qty: 3, Value: 30}}}, nil
}
func (f *fakeBroker) Place(_ context.Context, o Order, preview bool) (string, error) {
	f.placed = append(f.placed, o)
	return "ok", nil
}

func TestProposeApprove(t *testing.T) {
	st, err := LoadStore(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBroker{}
	e := &Engine{cfg: Config{MinTrade: 5, CashReserve: 5}, st: st, broker: b, notify: logNotifier{}, kick: make(chan struct{}, 1)}
	st.S.NeedPlan = "test"
	if err := e.Propose(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	p := st.pending()
	if p == nil || len(p.Orders) == 0 {
		t.Fatalf("no proposal: %+v", p)
	}
	if _, err := e.Decide(p.ID, true, "test"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50 && p.Status != "done"; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	st.mu.Lock()
	status, placed, n := p.Status, len(b.placed), len(p.Orders)
	st.mu.Unlock()
	if status != "done" || placed != n {
		t.Fatalf("status %s, placed %d of %d", status, placed, n)
	}
	if _, err := e.Decide(p.ID, true, "again"); err == nil {
		t.Fatal("a decided proposal must not be approved twice")
	}
}

func TestSleeveLeavesOtherHoldings(t *testing.T) {
	t.Setenv("INITIAL_CASH", "4.29")
	st, _ := LoadStore(filepath.Join(t.TempDir(), "s.json"))
	acct := Account{Cash: 500, Positions: []Position{{Symbol: "PANW", Qty: 1, Value: 216}, {Symbol: "MMM", Qty: 20, Value: 1234}, {Symbol: "XYZ", Qty: 30, Value: 41}}}
	sl := st.sleeveLocked(acct)
	if sl.Cash != 4.29 || len(sl.Positions) != 1 || sl.Positions[0].Symbol != "PANW" {
		t.Fatalf("%+v", sl)
	}
	for _, o := range Plan(st.S.Model.Weights(), sl, PlanOpts{MinTrade: 5, CashReserve: 5}) {
		if o.Symbol == "MMM" || o.Symbol == "XYZ" {
			t.Fatalf("traded a non-Pelosi holding: %v", o)
		}
	}
}
