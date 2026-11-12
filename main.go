// pelosi-mirror mirrors a member of Congress's disclosed trades (Nancy Pelosi's by
// default) into a Fidelity account, the way Autopilot's "Pelosi Tracker+" did, with
// the owner approving every set of orders first.
//
//   - Polls the House Clerk's member search for new Periodic Transaction Reports and
//     folds each disclosed trade into an estimated portfolio (model.go).
//   - When the model changes, reads the Fidelity account and proposes the orders that
//     bring it to the model's weights. The proposal goes to the owner as a Discord DM with
//     Approve/Reject buttons and to the web page.
//   - On approval, places the orders in Fidelity through the app's Chromium. Until live
//     trading is turned on (web page), orders stop at Fidelity's preview screen.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
	_ "time/tzdata"
)

type Config struct {
	MemberLast, MemberState string
	PollEvery               time.Duration
	MinTrade, CashReserve   float64
	WebPassword, Listen     string
	PublicURL, BrowserURL   string
	StateFile, DevTools     string
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envFloat(k string, d float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return d
}

func main() {
	cfg := Config{
		MemberLast:  envOr("MEMBER_LAST", "Pelosi"),
		MemberState: envOr("MEMBER_STATE", "CA"),
		PollEvery:   time.Duration(envFloat("POLL_MINUTES", 10)) * time.Minute,
		MinTrade:    envFloat("MIN_TRADE", 5),
		CashReserve: envFloat("CASH_RESERVE", 5),
		WebPassword: os.Getenv("WEB_PASSWORD"),
		Listen:      envOr("LISTEN", ":3013"),
		PublicURL:   os.Getenv("PUBLIC_URL"),
		BrowserURL:  os.Getenv("BROWSER_URL"),
		StateFile:   envOr("STATE_FILE", "/data/state.json"),
		DevTools:    envOr("DEVTOOLS_URL", "http://127.0.0.1:9222"),
	}
	if cfg.WebPassword == "" {
		log.Fatal("WEB_PASSWORD is required")
	}
	st, err := LoadStore(cfg.StateFile)
	if err != nil {
		log.Fatal(err)
	}
	fid := &Fidelity{devtools: cfg.DevTools, user: os.Getenv("FIDELITY_USERNAME"), pass: os.Getenv("FIDELITY_PASSWORD"),
		account: os.Getenv("FIDELITY_ACCOUNT")}
	e := &Engine{cfg: cfg, st: st, broker: fid, notify: logNotifier{}, kick: make(chan struct{}, 1)}
	if tok := os.Getenv("DISCORD_TOKEN"); tok != "" {
		d, err := NewDiscord(tok, os.Getenv("DISCORD_USER_ID"), cfg.PublicURL)
		if err != nil {
			log.Printf("discord disabled: %v", err)
		} else {
			d.engine = e
			e.notify = d
		}
	}
	web := &Web{cfg: cfg, st: st, engine: e}
	h := web.Handler().(*http.ServeMux)
	if os.Getenv("DEBUG") == "1" {
		web.debugRoutes(h, fid)
	}
	go e.Run(context.Background())
	if p := os.Getenv("SELF_WATCH"); p != "" {
		go selfWatch(p)
	}
	log.Printf("pelosi-mirror listening on %s (live trading %v)", cfg.Listen, st.S.Live)
	log.Fatal(http.ListenAndServe(cfg.Listen, h))
}

// selfWatch exits when the deployed binary changes; the container's entrypoint loop
// starts the new one. Deploys then only upload a file, and never restart the app,
// which would restart its Chromium and could sign Fidelity out.
func selfWatch(path string) {
	fi, err := os.Stat(path)
	if err != nil {
		log.Printf("self-watch: %v", err)
		return
	}
	for range time.Tick(15 * time.Second) {
		if n, err := os.Stat(path); err == nil && (n.ModTime() != fi.ModTime() || n.Size() != fi.Size()) {
			time.Sleep(3 * time.Second) // let the upload finish
			log.Printf("self-watch: new binary deployed, restarting")
			os.Exit(0)
		}
	}
}
