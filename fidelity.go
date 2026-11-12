package main

// Fidelity, driven through the Chromium in this app's "chromium" service over the
// DevTools protocol. A person signs in once through that browser's web desktop
// (Fidelity texts a code; tick "don't ask again on this device"); after that the
// saved profile keeps the device trusted, and the worker can sign back in with the
// username and password alone when the session times out.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

var ErrLogin = errors.New("Fidelity is signed out and needs a person to sign in (text-message code)")

const (
	fidLogin     = "https://digital.fidelity.com/prgw/digital/login/full-page"
	fidPositions = "https://digital.fidelity.com/ftgw/digital/portfolio/positions"
)

type Fidelity struct {
	devtools, user, pass, account string
	mu                            sync.Mutex
	lastAuto                      time.Time
	autoFailed                    bool
}

func browserWS(ctx context.Context, devtools string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", devtools+"/json/version", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("browser not reachable: %w", err)
	}
	defer resp.Body.Close()
	var v struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil || v.WS == "" {
		return "", errors.New("no devtools websocket")
	}
	return v.WS, nil
}

// inTab runs fn in a new tab of the signed-in browser, one action at a time.
func (f *Fidelity) inTab(ctx context.Context, timeout time.Duration, fn func(ctx context.Context) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ws, err := browserWS(ctx, f.devtools)
	if err != nil {
		return err
	}
	alloc, cancelAlloc := chromedp.NewRemoteAllocator(ctx, ws)
	defer cancelAlloc()
	tab, cancelTab := chromedp.NewContext(alloc)
	defer cancelTab()
	tctx, cancel := context.WithTimeout(tab, timeout)
	defer cancel()
	return fn(tctx)
}

func eval(ctx context.Context, js string, out any) error {
	return chromedp.Run(ctx, chromedp.Evaluate(js, out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
}

func location(ctx context.Context) string {
	var u string
	chromedp.Run(ctx, chromedp.Location(&u))
	return u
}

func onLogin(u string) bool {
	// Sign-in pages live under /prgw/digital/ (login/full-page, signin/retail, and the
	// text-message code pages); account pages are under /ftgw/.
	return strings.Contains(u, "/login") || strings.Contains(u, "/signin") || strings.Contains(u, "/prgw/digital/")
}

// signIn makes sure the tab is signed in, typing the saved credentials when the
// device is trusted. It never retries a failed attempt on its own: that waits for a
// person to sign in through the browser, so a wrong password can't lock the account.
func (f *Fidelity) signIn(ctx context.Context, target string) error {
	if err := chromedp.Run(ctx, chromedp.Navigate(target)); err != nil {
		return err
	}
	st := pageState(ctx)
	if st == "in" {
		f.autoFailed = false
		return nil
	}
	if st != "form" || f.user == "" || f.autoFailed || time.Since(f.lastAuto) < 15*time.Minute {
		log.Printf("fidelity: signed out (%s, %s)", st, location(ctx))
		return ErrLogin
	}
	f.lastAuto = time.Now()
	log.Printf("fidelity: signing in with saved credentials")
	err := chromedp.Run(ctx,
		chromedp.SendKeys(`#dom-username-input`, f.user, chromedp.ByQuery),
		chromedp.SendKeys(`#dom-pswd-input`, f.pass, chromedp.ByQuery),
		chromedp.Click(`#dom-login-button`, chromedp.ByQuery))
	// Stay on this page: navigating away would abandon a text-message code step.
	st = "wait"
	for i := 0; err == nil && i < 20 && st != "in"; i++ {
		time.Sleep(2 * time.Second)
		if location(ctx) != "" && !onLogin(location(ctx)) {
			st = pageState(ctx)
		}
	}
	if err != nil || st != "in" {
		f.autoFailed = true
		var png []byte
		if chromedp.Run(ctx, chromedp.FullScreenshot(&png, 70)) == nil {
			os.WriteFile(signinShot, png, 0o600)
		}
		log.Printf("fidelity: automatic sign-in did not finish (%v, %s); screenshot on the web page", err, location(ctx))
		return ErrLogin
	}
	log.Printf("fidelity: signed in with saved credentials")
	return nil
}

const signinShot = "/data/signin.jpg"

// pageState waits for a Fidelity page to settle: "in" (an account page), "form" (the
// username/password form) or "other" (a sign-in step such as the text-message code).
func pageState(ctx context.Context) string {
	const js = `document.querySelector("#dom-username-input") ? "form" :
		(location.pathname.startsWith("/ftgw/") && document.readyState === "complete") ? "in" : "wait"`
	st := "wait"
	for i := 0; i < 30; i++ {
		if err := eval(ctx, js, &st); err == nil && st != "wait" {
			// An account page can still bounce to sign-in; confirm it stays put.
			if st == "in" && i < 3 {
				time.Sleep(2 * time.Second)
				continue
			}
			return st
		}
		time.Sleep(time.Second)
	}
	if st == "wait" && onLogin(location(ctx)) {
		return "other"
	}
	return st
}

// Status reports whether the browser is signed in to Fidelity.
func (f *Fidelity) Status(ctx context.Context) error {
	return f.inTab(ctx, 2*time.Minute, func(ctx context.Context) error { return f.signIn(ctx, fidPositions) })
}

// positionsJS reads the positions grid (ag-grid). Its pinned symbol column and the
// scrolling value columns are separate DOM rows, joined here by row-index.
const positionsJS = `(async () => {
  for (let i = 0; i < 40 && !document.querySelector(".ag-row [col-id=curVal]"); i++) await new Promise(r => setTimeout(r, 500));
  const rows = {};
  for (const r of document.querySelectorAll(".ag-row")) {
    const i = +r.getAttribute("row-index");
    rows[i] = rows[i] || {};
    for (const c of r.querySelectorAll(".ag-cell")) rows[i][c.getAttribute("col-id")] = c.innerText.replace(/\s+/g, " ").trim();
  }
  return Object.keys(rows).map(Number).sort((a, b) => a - b).map(i => rows[i]);
})()`

func parseMoney(s string) float64 {
	s = strings.Fields(s + " ")[0] // first number in the cell ("$356.92 +$1.56")
	s = strings.NewReplacer("$", "", ",", "").Replace(s)
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// parsePositions picks the rows of one account out of the all-accounts grid.
func parsePositions(rows []map[string]string, account string) (Account, error) {
	var a Account
	in, found := false, false
	for _, r := range rows {
		sym := r["sym"]
		switch {
		case strings.HasPrefix(sym, "Account:"):
			in = account != "" && strings.Contains(strings.ReplaceAll(sym, " ", ""), account)
			found = found || in
			continue
		case !in || sym == "":
			continue
		case strings.HasPrefix(sym, "Account total"):
			in = false
			continue
		}
		f := strings.Fields(sym)
		if f[0] == "Cash" {
			a.Cash += parseMoney(r["curVal"])
			continue
		}
		qty, _ := strconv.ParseFloat(strings.ReplaceAll(strings.Fields(r["qty"] + " 0")[0], ",", ""), 64)
		a.Positions = append(a.Positions, Position{Symbol: f[0], Qty: qty, Value: parseMoney(r["curVal"])})
	}
	if !found {
		return a, fmt.Errorf("account %q not found on the positions page", account)
	}
	return a, nil
}

func (f *Fidelity) Snapshot(ctx context.Context) (Account, error) {
	var a Account
	err := f.inTab(ctx, 3*time.Minute, func(ctx context.Context) error {
		if err := f.signIn(ctx, fidPositions); err != nil {
			return err
		}
		var rows []map[string]string
		if err := eval(ctx, positionsJS, &rows); err != nil {
			return err
		}
		var err error
		a, err = parsePositions(rows, f.account)
		return err
	})
	return a, err
}

const fidTrade = "https://digital.fidelity.com/ftgw/digital/trade-equity/index/orderEntry"

// ticketJS fills Fidelity's stock ticket and clicks Preview order, then reads the
// preview back. Fidelity's dropdowns only respond to full pointer/mouse event
// sequences, and choosing an action resets the quantity type and order type, so the
// fields go in this order and are re-checked until they all stick.
const ticketJS = `(async (o) => {
  const sleep = ms => new Promise(r => setTimeout(r, ms));
  const $ = s => document.querySelector(s);
  const txt = s => ($(s)?.innerText || "").trim().replace(/\s+/g, " ");
  const real = el => { const r = el.getBoundingClientRect(), e = {bubbles: true, cancelable: true, clientX: r.x + r.width/2, clientY: r.y + r.height/2, button: 0};
    for (const t of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) el.dispatchEvent(new (t.startsWith("pointer") ? PointerEvent : MouseEvent)(t, e)); };
  const typeInto = (el, v) => { el.focus(); Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(el, v);
    el.dispatchEvent(new Event("input", {bubbles: true})); el.dispatchEvent(new Event("change", {bubbles: true})); };
  const opts = () => [...document.querySelectorAll("[role=option]")].filter(e => e.offsetParent);
  const choose = async (btn, match, want) => {
    for (let i = 0; i < 4; i++) {
      if (txt(btn).endsWith(want)) return true;
      real($(btn));
      let el; for (let j = 0; j < 12 && !(el = opts().find(match)); j++) await sleep(300);
      if (el) real(el);
      await sleep(1500);
    }
    return txt(btn).endsWith(want);
  };
  for (let i = 0; i < 40 && !$("#dest-acct-dropdown"); i++) await sleep(500);
  if (!$("#dest-acct-dropdown")) return {error: "trade ticket did not load"};
  if (!await choose("#dest-acct-dropdown", e => e.tagName === "BUTTON" && e.innerText.includes("(" + o.account + ")"), "(" + o.account + ")"))
    return {error: "could not select account " + o.account};
  typeInto($("#eq-ticket-dest-symbol"), o.symbol);
  let sug; for (let i = 0; i < 25 && !(sug = opts().find(e => e.id.startsWith("suggestion-item") && e.innerText.trim().split(/\s/)[0] === o.symbol)); i++) await sleep(300);
  if (!sug) return {error: "symbol " + o.symbol + " not found"};
  real(sug); await sleep(4000);
  const action = o.side === "buy" ? "Buy" : "Sell", qtype = o.dollars ? "Dollars" : "Shares";
  let ok = false;
  for (let round = 0; round < 3 && !ok; round++) {
    await choose("#dest-dropdownlist-button-action", e => e.id.startsWith("Action") && e.innerText.trim() === action, action);
    await choose("[data-testid=quantity-type-trigger]", e => e.id.startsWith("quantity-option") && e.innerText.trim() === qtype, qtype);
    const q = $("#eqt-shared-quantity");
    if (parseFloat(q.value.replace(/[$,]/g, "")) !== parseFloat(o.qty)) { typeInto(q, o.qty); await sleep(1200); }
    await choose("#dest-dropdownlist-button-ordertype", e => e.id.startsWith("Order-type") && e.innerText.trim() === "Market", "Market");
    ok = txt("#dest-dropdownlist-button-action").endsWith(action) && txt("[data-testid=quantity-type-trigger]") === qtype &&
      txt("#dest-dropdownlist-button-ordertype").endsWith("Market") && parseFloat(q.value.replace(/[$,]/g, "")) === parseFloat(o.qty);
  }
  if (!ok) return {error: "the ticket fields would not stay set"};
  real([...document.querySelectorAll("button")].find(e => e.innerText.trim() === "Preview order"));
  for (let i = 0; i < 30 && !$("#placeOrderBtn"); i++) await sleep(500);
  const body = document.body.innerText;
  const i = body.indexOf("Preview Order");
  if (!$("#placeOrderBtn") || i < 0) return {error: "no preview: " + body.split("Give feedback")[1]?.replace(/\n+/g, " | ").slice(0, 600)};
  const p = body.slice(i).split("\n").map(s => s.trim()).filter(Boolean);
  const field = k => p[p.indexOf(k) + 1] || "";
  return {account: field("Account"), symbol: field("Symbol"), action: field("Action"), quantity: field("Quantity"),
    orderType: field("Order type"), estimate: (body.match(/Estimated order value[^\n]*\n+\s*([^\n]+)/) || [])[1] || "",
    preview: p.slice(0, 24).join(" | ")};
})`

type ticketResult struct {
	Error, Account, Symbol, Action, Quantity, OrderType, Estimate, Preview string
}

func orderQty(o Order) (qty string, dollars bool) {
	if o.Side == "buy" {
		return strconv.FormatFloat(o.Dollars, 'f', 2, 64), true
	}
	return strconv.FormatFloat(o.Shares, 'f', 3, 64), false
}

// checkPreview makes sure Fidelity's preview is exactly the approved order.
func checkPreview(r ticketResult, o Order, account string) error {
	qty, dollars := orderQty(o)
	want := map[string]string{"account": account, "symbol": o.Symbol, "action": map[string]string{"buy": "Buy", "sell": "Sell"}[o.Side], "order type": "Market"}
	got := map[string]string{"account": r.Account, "symbol": r.Symbol, "action": r.Action, "order type": r.OrderType}
	for k, v := range want {
		if k == "account" {
			if !strings.Contains(got[k], "("+v+")") {
				return fmt.Errorf("preview shows account %q", got[k])
			}
		} else if got[k] != v {
			return fmt.Errorf("preview shows %s %q, expected %q", k, got[k], v)
		}
	}
	gq := parseMoney(r.Quantity)
	wq, _ := strconv.ParseFloat(qty, 64)
	if math.Abs(gq-wq) > 0.0005 || (dollars != strings.HasPrefix(r.Quantity, "$")) {
		return fmt.Errorf("preview shows quantity %q, expected %s (dollars=%v)", r.Quantity, qty, dollars)
	}
	return nil
}

var confirmRe = regexp.MustCompile(`(?i)order (?:number|#|no\.?)\s*:?\s*([A-Z0-9-]{5,})`)

func (f *Fidelity) Place(ctx context.Context, o Order, preview bool) (string, error) {
	if f.account == "" {
		return "", errors.New("FIDELITY_ACCOUNT is not set")
	}
	var res string
	err := f.inTab(ctx, 4*time.Minute, func(ctx context.Context) error {
		if err := f.signIn(ctx, fidTrade); err != nil {
			return err
		}
		qty, dollars := orderQty(o)
		arg, _ := json.Marshal(map[string]any{"account": f.account, "symbol": o.Symbol, "side": o.Side, "qty": qty, "dollars": dollars})
		var r ticketResult
		if err := eval(ctx, ticketJS+"("+string(arg)+")", &r); err != nil {
			return err
		}
		if r.Error != "" {
			return errors.New(r.Error)
		}
		if err := checkPreview(r, o, f.account); err != nil {
			return err
		}
		log.Printf("fidelity: preview ok: %s", r.Preview)
		if preview {
			res = "preview OK, est. " + r.Estimate
			return nil
		}
		var body string
		err := chromedp.Run(ctx,
			chromedp.Click(`#placeOrderBtn`, chromedp.ByQuery),
			chromedp.Sleep(8*time.Second),
			chromedp.Evaluate(`document.body.innerText`, &body))
		f.shot(ctx, fmt.Sprintf("/data/orders/%s-%s-%s.jpg", time.Now().Format("20060102-150405"), o.Side, o.Symbol))
		if err != nil {
			return fmt.Errorf("after clicking Place order: %w (check Activity & Orders)", err)
		}
		if m := confirmRe.FindStringSubmatch(body); m != nil {
			res = "order " + m[1]
			return nil
		}
		if i := strings.Index(body, "Order received"); i >= 0 {
			res = "order received"
			return nil
		}
		snippet := body
		if i := strings.Index(body, "Give feedback"); i >= 0 {
			snippet = body[i:]
		}
		if len(snippet) > 400 {
			snippet = snippet[:400]
		}
		return fmt.Errorf("placed, but the confirmation wasn't recognized; check Activity & Orders before retrying: %s", strings.Join(strings.Fields(snippet), " "))
	})
	return res, err
}

func (f *Fidelity) shot(ctx context.Context, path string) {
	var png []byte
	if chromedp.Run(ctx, chromedp.FullScreenshot(&png, 70)) == nil {
		os.MkdirAll("/data/orders", 0o700)
		os.WriteFile(path, png, 0o600)
	}
}
