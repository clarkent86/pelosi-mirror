package main

// Development endpoints (DEBUG=1 only, behind the web password) for building the
// Fidelity driver against the live site: a screenshot of a page, and evaluating
// JavaScript in it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/chromedp/chromedp"
)

func (w *Web) debugRoutes(mux *http.ServeMux, f *Fidelity) {
	run := func(r *http.Request, fn func(ctx context.Context) error) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		return f.inTab(ctx, 3*time.Minute, func(ctx context.Context) error {
			if u := r.FormValue("url"); u != "" {
				wait, _ := strconv.Atoi(r.FormValue("wait"))
				if wait == 0 {
					wait = 6
				}
				if err := chromedp.Run(ctx, chromedp.Navigate(u), chromedp.Sleep(time.Duration(wait)*time.Second)); err != nil {
					return err
				}
			}
			return fn(ctx)
		})
	}
	mux.HandleFunc("/debug/shot", w.auth(func(rw http.ResponseWriter, r *http.Request) {
		var png []byte
		var loc string
		err := run(r, func(ctx context.Context) error {
			loc = location(ctx)
			return chromedp.Run(ctx, chromedp.FullScreenshot(&png, 70))
		})
		if err != nil {
			http.Error(rw, err.Error(), 502)
			return
		}
		rw.Header().Set("X-Location", loc)
		rw.Header().Set("Content-Type", "image/jpeg")
		rw.Write(png)
	}))
	mux.HandleFunc("/debug/preview", w.auth(func(rw http.ResponseWriter, r *http.Request) {
		amt, _ := strconv.ParseFloat(r.FormValue("qty"), 64)
		o := Order{Symbol: r.FormValue("symbol"), Side: r.FormValue("side")}
		if o.Side == "buy" {
			o.Dollars = amt
		} else {
			o.Shares = amt
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		res, err := f.Place(ctx, o, true) // preview only, never places
		json.NewEncoder(rw).Encode(map[string]any{"order": o.String(), "result": res, "error": fmt.Sprint(err)})
	}))
	mux.HandleFunc("/debug/eval", w.auth(func(rw http.ResponseWriter, r *http.Request) {
		var out any
		var loc string
		err := run(r, func(ctx context.Context) error {
			loc = location(ctx)
			if js := r.FormValue("js"); js != "" {
				return eval(ctx, js, &out)
			}
			return nil
		})
		res := map[string]any{"location": loc, "result": out}
		if err != nil {
			res["error"] = err.Error()
		}
		json.NewEncoder(rw).Encode(res)
	}))
}
