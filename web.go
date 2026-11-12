package main

import (
	"context"
	"crypto/subtle"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"
)

type Web struct {
	cfg    Config
	st     *Store
	engine *Engine
}

func (w *Web) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		_, pw, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pw), []byte(w.cfg.WebPassword)) != 1 {
			rw.Header().Set("WWW-Authenticate", `Basic realm="pelosi-mirror"`)
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(rw, r)
	}
}

func (w *Web) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", w.auth(w.index))
	mux.HandleFunc("POST /decide", w.auth(w.decide))
	mux.HandleFunc("POST /action", w.auth(w.action))
	mux.HandleFunc("GET /signin.jpg", w.auth(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "image/jpeg")
		http.ServeFile(rw, r, signinShot)
	}))
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, r *http.Request) { rw.Write([]byte("ok")) })
	return mux
}

type weightRow struct {
	Sym          string
	Weight, Have float64
	Model        float64
}

func (w *Web) index(rw http.ResponseWriter, r *http.Request) {
	w.st.mu.Lock()
	s := w.st.S
	weights := s.Model.Weights()
	held := map[string]float64{}
	total := 0.0
	var sleeve *Account
	if s.Account != nil {
		sl := w.st.sleeveLocked(*s.Account)
		sleeve = &sl
		total = sl.Total()
		for _, p := range sl.Positions {
			held[p.Symbol] = p.Value
		}
	}
	var rows []weightRow
	seen := map[string]bool{}
	for k, v := range weights {
		rows = append(rows, weightRow{Sym: k, Weight: v, Have: held[k], Model: s.Model[k]})
		seen[k] = true
	}
	for k, v := range held {
		if !seen[k] {
			rows = append(rows, weightRow{Sym: k, Have: v})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Weight > rows[j].Weight })
	props := append([]*Proposal(nil), s.Proposals...)
	sort.Slice(props, func(i, j int) bool { return props[i].ID > props[j].ID })
	if len(props) > 15 {
		props = props[:15]
	}
	txns := s.Txns
	if len(txns) > 40 {
		txns = txns[len(txns)-40:]
	}
	data := map[string]any{"S": s, "Rows": rows, "Total": total, "Props": props, "Txns": txns, "Cfg": w.cfg, "Sleeve": sleeve, "Log": recent.Last(40),
		"Now": time.Now()}
	err := page.Execute(rw, data)
	w.st.mu.Unlock()
	if err != nil {
		http.Error(rw, err.Error(), 500)
	}
}

func (w *Web) decide(rw http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.FormValue("id"))
	if _, err := w.engine.Decide(id, r.FormValue("approve") == "1", "web"); err != nil {
		http.Error(rw, err.Error(), 409)
		return
	}
	http.Redirect(rw, r, "/", http.StatusSeeOther)
}

func (w *Web) action(rw http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var err error
	switch r.FormValue("do") {
	case "poll":
		w.engine.Kick()
	case "refresh":
		_, err = w.engine.Refresh(ctx)
	case "live", "dry":
		w.st.mu.Lock()
		w.st.S.Live = r.FormValue("do") == "live"
		w.st.saveLocked()
		w.st.mu.Unlock()
		log.Printf("live trading %v (web)", r.FormValue("do") == "live")
	case "cash":
		v, perr := strconv.ParseFloat(r.FormValue("cash"), 64)
		if perr != nil || v < 0 {
			http.Error(rw, "cash must be a number", 400)
			return
		}
		w.st.mu.Lock()
		w.st.S.SleeveCash = v
		w.st.saveLocked()
		w.st.mu.Unlock()
		log.Printf("strategy cash set to $%.2f (web)", v)
	case "propose":
		err = w.engine.Propose(ctx, "Manual rebalance")
	}
	if err != nil {
		http.Error(rw, err.Error(), 502)
		return
	}
	http.Redirect(rw, r, "/", http.StatusSeeOther)
}

var page = template.Must(template.New("p").Funcs(template.FuncMap{
	"pct":   func(f float64) string { return strconv.FormatFloat(f*100, 'f', 1, 64) + "%" },
	"money": money,
	"human": human,
	"rng":   rangeStr,
	"mul":   func(a, b float64) float64 { return a * b },
	"when":  func(t time.Time) string { return t.Local().Format("Jan 2 3:04 PM") },
}).Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>pelosi-mirror</title><style>
body{font:15px system-ui,sans-serif;max-width:960px;margin:0 auto;padding:16px;color:#1d1d1f;background:#fafafa}
h1{font-size:22px}h2{font-size:17px;margin-top:28px}table{border-collapse:collapse;width:100%;background:#fff}
td,th{padding:6px 8px;border-bottom:1px solid #eee;text-align:left}td.n,th.n{text-align:right;font-variant-numeric:tabular-nums}
.card{background:#fff;border:1px solid #e3e3e3;border-radius:10px;padding:14px;margin:12px 0}
.err{background:#fff3f3;border-color:#f3c2c2}.muted{color:#777;font-size:13px}button{font:inherit;padding:6px 14px;border-radius:7px;border:1px solid #ccc;background:#fff;cursor:pointer}
button.ok{background:#1f8b4c;color:#fff;border-color:#1f8b4c}button.no{background:#c0392b;color:#fff;border-color:#c0392b}
form{display:inline}pre{white-space:pre-wrap;margin:6px 0}
</style></head><body>
<h1>Pelosi mirror</h1>
<div class="muted">Last filing check {{if .S.LastPoll.IsZero}}never{{else}}{{when .S.LastPoll}}{{end}}
{{if .S.Account}} · positions as of {{.S.Account.AsOf}}{{end}}
{{if .S.Live}} · <b style="color:#c0392b">LIVE trading</b>: approved orders are placed{{else}} · <b>dry run</b>: approved orders stop at Fidelity's preview{{end}}</div>
{{if .S.LastError}}<div class="card err">{{.S.LastError}}{{if .Cfg.BrowserURL}} — <a href="{{.Cfg.BrowserURL}}" target="_blank">open the Fidelity browser</a>{{end}} · <a href="/signin.jpg" target="_blank">last automatic sign-in</a></div>{{end}}
{{if .S.NeedPlan}}<div class="card">Rebalance due:<pre>{{.S.NeedPlan}}</pre></div>{{end}}
<div>
<form method="post" action="/action"><input type="hidden" name="do" value="poll"><button>Check filings now</button></form>
<form method="post" action="/action"><input type="hidden" name="do" value="refresh"><button>Refresh positions</button></form>
<form method="post" action="/action"><input type="hidden" name="do" value="propose"><button>Propose rebalance</button></form>
{{if .Cfg.BrowserURL}}<a href="{{.Cfg.BrowserURL}}" target="_blank"><button>Fidelity browser</button></a>{{end}}
{{if .S.Live}}<form method="post" action="/action"><input type="hidden" name="do" value="dry"><button>Switch to dry run</button></form>
{{else}}<form method="post" action="/action" onsubmit="return confirm('Turn on live trading? Proposals created after this place real orders when approved.')"><input type="hidden" name="do" value="live"><button class="no">Turn on live trading</button></form>{{end}}
</div>

{{range .Props}}{{if eq .Status "pending" "approved" "executing"}}
<div class="card"><b>Proposal #{{.ID}}</b> · {{.Status}}{{if .DryRun}} (dry run){{end}} · {{when .Created}} · account {{money .Total}}
<pre>{{.Reason}}</pre>
<table><tr><th>Order</th><th class="n">Now</th><th class="n">Target</th><th>Status</th></tr>
{{range .Orders}}<tr><td>{{.}}</td><td class="n">{{money .From}}</td><td class="n">{{money .To}}</td><td>{{.Status}} {{.Result}}</td></tr>{{end}}</table>
{{if eq .Status "pending"}}<p>
<form method="post" action="/decide"><input type="hidden" name="id" value="{{.ID}}"><input type="hidden" name="approve" value="1"><button class="ok">Approve and place orders</button></form>
<form method="post" action="/decide"><input type="hidden" name="id" value="{{.ID}}"><input type="hidden" name="approve" value="0"><button class="no">Reject</button></form></p>{{end}}
</div>{{end}}{{end}}

<h2>Strategy portfolio{{if .Total}} ({{money .Total}}){{end}}</h2>
<div class="muted">Only these tickers and the strategy cash are traded; the rest of the account{{if .S.Account}} ({{money .S.Account.Total}} in all){{end}} is left alone.</div>
<table><tr><th>Ticker</th><th class="n">Target</th><th class="n">Target $</th><th class="n">You hold</th><th class="n">Model (est. Pelosi $)</th></tr>
{{$t := .Total}}{{range .Rows}}<tr><td>{{.Sym}}</td><td class="n">{{pct .Weight}}</td><td class="n">{{if $t}}{{money (mul .Weight $t)}}{{end}}</td><td class="n">{{if .Have}}{{money .Have}}{{end}}</td><td class="n">{{if .Model}}{{human .Model}}{{end}}</td></tr>{{end}}
{{if .Sleeve}}<tr><td>Cash (strategy share)</td><td></td><td></td><td class="n">{{money .Sleeve.Cash}}</td><td></td></tr>{{end}}
</table>
<form method="post" action="/action" style="margin-top:8px"><input type="hidden" name="do" value="cash">
Strategy cash: $<input name="cash" value="{{printf "%.2f" .S.SleeveCash}}" size="9" inputmode="decimal">
<button>Save</button> <span class="muted">How much of the account's cash the strategy may invest. Raise it when you add money, then press Propose rebalance.</span></form>

<h2>History</h2>
<table><tr><th>#</th><th>Created</th><th>Status</th><th>Orders</th></tr>
{{range .Props}}<tr><td>{{.ID}}</td><td>{{when .Created}}</td><td>{{.Status}}{{if .DryRun}} (dry){{end}}</td><td>{{range .Orders}}{{.}} <span class="muted">{{.Status}}</span><br>{{end}}</td></tr>{{end}}
</table>

<h2>Disclosed trades seen</h2>
<table><tr><th>Filing</th><th>Date</th><th>Trade</th><th>Range</th><th>Model</th></tr>
{{range .Txns}}<tr><td>{{.Filing}}</td><td>{{.Date}}</td><td>{{.Type}} {{.Ticker}} [{{.AssetType}}]<div class="muted">{{.Desc}}</div></td><td>{{rng .Txn}}</td><td>{{.Note}}</td></tr>{{end}}
</table>

<h2>Log</h2>
<pre class="muted">{{range .Log}}{{.}}
{{end}}</pre>
</body></html>`))
