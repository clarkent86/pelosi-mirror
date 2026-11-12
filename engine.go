package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

type Broker interface {
	// Snapshot reads the account's positions and cash. ErrLogin means a person must sign in.
	Snapshot(ctx context.Context) (Account, error)
	// Place submits an order (or, with preview, stops at Fidelity's preview) and returns its confirmation.
	Place(ctx context.Context, o Order, preview bool) (string, error)
}

type Notifier interface {
	Proposal(p *Proposal) (msgID string)
	Update(p *Proposal)
	Say(text string)
}

type Engine struct {
	cfg    Config
	st     *Store
	broker Broker
	notify Notifier
	kick   chan struct{}
}

func (e *Engine) Kick() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// Run polls for filings and plans rebalances until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	for {
		e.cycle(ctx)
		wait := e.cfg.PollEvery
		if h := time.Now().Hour(); h < 6 || h >= 22 {
			wait = time.Hour
		}
		select {
		case <-ctx.Done():
			return
		case <-e.kick:
		case <-time.After(wait):
		}
	}
}

func (e *Engine) cycle(ctx context.Context) {
	if err := e.poll(ctx); err != nil {
		log.Printf("poll: %v", err)
		e.setErr("filings: " + err.Error())
	}
	e.st.mu.Lock()
	need := e.st.S.NeedPlan != "" && e.st.pending() == nil && !e.busyLocked()
	e.st.mu.Unlock()
	if need {
		if err := e.Propose(ctx, ""); err != nil {
			log.Printf("propose: %v", err)
		}
	}
}

func (e *Engine) setErr(s string) {
	e.st.mu.Lock()
	e.st.S.LastError = s
	e.st.saveLocked()
	e.st.mu.Unlock()
}

func (e *Engine) busyLocked() bool {
	for _, p := range e.st.S.Proposals {
		if p.Status == "approved" || p.Status == "executing" {
			return true
		}
	}
	return false
}

// poll looks for new filings and folds them into the model.
func (e *Engine) poll(ctx context.Context) error {
	years := []int{time.Now().Year()}
	if time.Now().Month() <= time.February { // late filings for last year's trades
		years = append([]int{time.Now().Year() - 1}, years...)
	}
	var fresh []Filing
	for _, y := range years {
		fs, err := SearchPTRs(ctx, e.cfg.MemberLast, e.cfg.MemberState, y)
		if err != nil {
			return err
		}
		e.st.mu.Lock()
		for _, f := range fs {
			if !e.st.S.Applied[f.ID] {
				fresh = append(fresh, f)
			}
		}
		e.st.mu.Unlock()
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].ID < fresh[j].ID })
	for _, f := range fresh {
		txns, err := FetchPTR(ctx, f)
		if err != nil {
			return fmt.Errorf("filing %s: %w", f.ID, err)
		}
		// Oldest trade first, so a buy then sell of the same stock nets out right.
		sort.SliceStable(txns, func(i, j int) bool { return dateKey(txns[i].Date) < dateKey(txns[j].Date) })
		var changed []string
		e.st.mu.Lock()
		for _, t := range txns {
			ok, note := e.st.S.Model.Apply(t)
			e.st.S.Txns = append(e.st.S.Txns, TxnNote{Txn: t, Applied: ok, Note: note})
			if ok {
				if e.st.S.Model[t.Ticker] > 0 {
					e.st.S.Managed[t.Ticker] = true
				}
				changed = append(changed, fmt.Sprintf("%s %s %s", sideWord(t.Type), t.Ticker, rangeStr(t)))
			}
		}
		e.st.S.Applied[f.ID] = true
		if len(changed) > 0 {
			r := fmt.Sprintf("Filing %s: %s", f.ID, strings.Join(changed, "; "))
			if e.st.S.NeedPlan != "" {
				r = e.st.S.NeedPlan + "\n" + r
			}
			e.st.S.NeedPlan = r
		}
		e.st.saveLocked()
		e.st.mu.Unlock()
		log.Printf("filing %s: %d transactions, %d mirrored", f.ID, len(txns), len(changed))
		if len(changed) == 0 {
			e.notify.Say(fmt.Sprintf("New Pelosi filing %s (%d transactions), nothing to mirror. %s", f.ID, len(txns), f.URL()))
		}
	}
	e.st.mu.Lock()
	e.st.S.LastPoll = time.Now()
	if strings.HasPrefix(e.st.S.LastError, "filings:") {
		e.st.S.LastError = ""
	}
	e.st.saveLocked()
	e.st.mu.Unlock()
	return nil
}

func dateKey(d string) string { // MM/DD/YYYY -> YYYYMMDD
	if len(d) == 10 {
		return d[6:] + d[:2] + d[3:5]
	}
	return d
}

func sideWord(t string) string {
	switch t {
	case "P":
		return "bought"
	case "S":
		return "sold"
	case "S (partial)":
		return "sold part of"
	}
	return t
}

func rangeStr(t Txn) string {
	return fmt.Sprintf("($%s-$%s)", human(float64(t.Low)), human(float64(t.High)))
}

// Refresh re-reads the account from the broker.
func (e *Engine) Refresh(ctx context.Context) (Account, error) {
	a, err := e.broker.Snapshot(ctx)
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	if err != nil {
		e.st.S.LastError = "fidelity: " + err.Error()
		if err == ErrLogin && time.Since(e.st.S.LoginNagged) > 6*time.Hour {
			e.st.S.LoginNagged = time.Now()
			go e.notify.Say("Fidelity needs you to sign in before I can read positions or trade: " + e.cfg.BrowserURL)
		}
		e.st.saveLocked()
		return a, err
	}
	a.AsOf = time.Now().Format(time.RFC3339)
	e.st.S.Account = &a
	if strings.HasPrefix(e.st.S.LastError, "fidelity:") {
		e.st.S.LastError = ""
	}
	e.st.saveLocked()
	return a, nil
}

// Propose reads the account and posts a rebalance proposal (replacing any pending one).
func (e *Engine) Propose(ctx context.Context, why string) error {
	acct, err := e.Refresh(ctx)
	if err != nil {
		return err
	}
	e.st.mu.Lock()
	if e.busyLocked() {
		e.st.mu.Unlock()
		return fmt.Errorf("orders are being placed right now")
	}
	reason := e.st.S.NeedPlan
	if reason == "" {
		reason = why
	}
	if reason == "" {
		reason = "Manual rebalance"
	}
	w := e.st.S.Model.Weights()
	acct = e.st.sleeveLocked(acct)
	orders := Plan(w, acct, PlanOpts{MinTrade: e.cfg.MinTrade, CashReserve: e.cfg.CashReserve})
	e.st.S.NeedPlan = ""
	if len(orders) == 0 {
		e.st.saveLocked()
		e.st.mu.Unlock()
		e.notify.Say(reason + "\nYour account already matches the targets; no trades needed.")
		return nil
	}
	var old *Proposal
	if p := e.st.pending(); p != nil {
		p.Status, p.Decided = "superseded", time.Now()
		old = p
	}
	p := &Proposal{ID: e.st.S.NextID, Created: time.Now(), Reason: reason, Orders: orders, Weights: w,
		Total: acct.Total(), Status: "pending", DryRun: !e.st.S.Live}
	e.st.S.NextID++
	e.st.S.Proposals = append(e.st.S.Proposals, p)
	e.st.saveLocked()
	e.st.mu.Unlock()
	if old != nil {
		e.notify.Update(old)
	}
	id := e.notify.Proposal(p)
	e.st.mu.Lock()
	p.MsgID = id
	e.st.saveLocked()
	e.st.mu.Unlock()
	return nil
}

// Decide approves or rejects a pending proposal. Approval places the orders.
func (e *Engine) Decide(id int, approve bool, who string) (string, error) {
	e.st.mu.Lock()
	p := e.st.proposal(id)
	if p == nil || p.Status != "pending" {
		e.st.mu.Unlock()
		return "", fmt.Errorf("proposal #%d is not pending", id)
	}
	p.Decided = time.Now()
	if !approve {
		p.Status = "rejected"
		p.Log = append(p.Log, "Rejected by "+who)
		e.st.saveLocked()
		e.st.mu.Unlock()
		e.notify.Update(p)
		return "Rejected. No orders placed.", nil
	}
	p.Status = "approved"
	p.Log = append(p.Log, "Approved by "+who)
	e.st.saveLocked()
	e.st.mu.Unlock()
	e.notify.Update(p)
	go e.execute(p)
	if p.DryRun {
		return "Approved. Dry run: orders go to Fidelity's preview only and are not submitted.", nil
	}
	return "Approved. Placing orders in Fidelity now.", nil
}

func (e *Engine) execute(p *Proposal) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	set := func(f func()) {
		e.st.mu.Lock()
		f()
		e.st.saveLocked()
		e.st.mu.Unlock()
	}
	set(func() { p.Status = "executing" })
	failed := false
	for i := range p.Orders {
		o := p.Orders[i]
		if failed {
			set(func() { p.Orders[i].Status = "skipped" })
			continue
		}
		conf, err := e.broker.Place(ctx, o, p.DryRun)
		set(func() {
			if err != nil {
				p.Orders[i].Status, p.Orders[i].Result = "failed", err.Error()
				p.Log = append(p.Log, fmt.Sprintf("%s failed: %v", o, err))
				failed = true
			} else {
				p.Orders[i].Status, p.Orders[i].Result = "placed", conf
				if p.DryRun {
					p.Orders[i].Status = "previewed"
				} else if o.Side == "sell" {
					e.st.S.SleeveCash += o.Dollars
				} else {
					e.st.S.SleeveCash = math.Max(0, e.st.S.SleeveCash-o.Dollars)
				}
			}
		})
		log.Printf("proposal %d: %s -> %v %v", p.ID, o, conf, err)
	}
	set(func() {
		p.Status = "done"
		if failed {
			p.Status = "failed"
		}
	})
	e.notify.Update(p)
	if !p.DryRun {
		time.Sleep(time.Minute)
		e.Refresh(ctx)
	}
}
