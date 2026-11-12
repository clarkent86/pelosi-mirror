package main

// Reading Periodic Transaction Reports (PTRs) from the House Clerk.
//
// The Clerk's member search lists every filing as soon as it is accepted, with a
// link to its PDF. E-filed PTR PDFs keep their text inside a Form XObject, which
// internal/pdf (a patched copy of ledongthuc/pdf) follows.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"pelosimirror/internal/pdf"
)

const clerk = "https://disclosures-clerk.house.gov"

// Filing is one PTR in the Clerk's member search.
type Filing struct {
	ID   string `json:"id"`
	Year int    `json:"year"`
}

func (f Filing) URL() string {
	return fmt.Sprintf("%s/public_disc/ptr-pdfs/%d/%s.pdf", clerk, f.Year, f.ID)
}

// Txn is one transaction row in a PTR.
type Txn struct {
	Filing    string `json:"filing"`
	Owner     string `json:"owner"` // SP spouse, JT joint, DC dependent child, "" self
	Asset     string `json:"asset"`
	Ticker    string `json:"ticker"`
	AssetType string `json:"asset_type"` // ST stock, OP option, ...
	Type      string `json:"type"`       // P, S, S (partial), E
	Date      string `json:"date"`       // MM/DD/YYYY
	Low       int64  `json:"low"`
	High      int64  `json:"high"`
	Desc      string `json:"desc"`
}

func (t Txn) Mid() float64 { return float64(t.Low+t.High) / 2 }

var httpc = &http.Client{Timeout: 60 * time.Second}

func get(ctx context.Context, method, u string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pelosi-mirror (personal use)")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s %s: HTTP %d", method, u, resp.StatusCode)
	}
	return b, nil
}

var ptrLink = regexp.MustCompile(`ptr-pdfs/(\d{4})/(\d+)\.pdf`)

// SearchPTRs lists the member's PTR filings for a year.
func SearchPTRs(ctx context.Context, last, state string, year int) ([]Filing, error) {
	form := url.Values{"LastName": {last}, "FilingYear": {strconv.Itoa(year)}, "State": {state}, "District": {""}}
	b, err := get(ctx, "POST", clerk+"/FinancialDisclosure/ViewMemberSearchResult", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Filing
	for _, m := range ptrLink.FindAllStringSubmatch(string(b), -1) {
		if !seen[m[2]] {
			seen[m[2]] = true
			y, _ := strconv.Atoi(m[1])
			out = append(out, Filing{ID: m[2], Year: y})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// FetchPTR downloads and parses one filing.
func FetchPTR(ctx context.Context, f Filing) ([]Txn, error) {
	b, err := get(ctx, "GET", f.URL(), nil)
	if err != nil {
		return nil, err
	}
	txns, err := ParsePTR(b)
	for i := range txns {
		txns[i].Filing = f.ID
	}
	return txns, err
}

// pdfLines returns the text of each visual line, top to bottom, page by page.
func pdfLines(data []byte) (lines []string, err error) {
	defer func() {
		if r := recover(); r != nil { // the pdf package panics on malformed input
			err = fmt.Errorf("pdf: %v", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for i := 1; i <= r.NumPage(); i++ {
		t := r.Page(i).Content().Text
		sort.Stable(pdf.TextVertical(t))
		var b strings.Builder
		y := math.NaN()
		for _, c := range t {
			if !math.IsNaN(y) && math.Abs(c.Y-y) > 1 {
				lines = append(lines, b.String())
				b.Reset()
			}
			y = c.Y
			b.WriteString(strings.ReplaceAll(c.S, "\x00", "")) // small-caps font with no text mapping
		}
		if b.Len() > 0 {
			lines = append(lines, b.String())
		}
	}
	return lines, nil
}

var (
	rowRe    = regexp.MustCompile(`^(SP|JT|DC)?(.*?)(S \(partial\)|P|S|E)\s*(\d{2}/\d{2}/\d{4})\s*(\d{2}/\d{2}/\d{4})\s*(.*)$`)
	tailAmt  = regexp.MustCompile(`\s*((?:Over )?\$[\d,]+(?:\s*-)?)\s*$`)
	moneyRe  = regexp.MustCompile(`\$([\d,]+)`)
	tickerRe = regexp.MustCompile(`\(([A-Z][A-Z0-9.\-]{0,9})\)\s*\[([A-Z]{2})\]`)
	typeRe   = regexp.MustCompile(`\[([A-Z]{2})\]`)
	descRe   = regexp.MustCompile(`^D\s*:\s*(.*)$`)
	statusRe = regexp.MustCompile(`^F\s*S\s*:`)
)

func isHeader(l string) bool {
	for _, p := range []string{"IDOwner", "ID Owner", "TypeDate", "Type Date", "Type", "Date", "$200?", "Filing ID", "Clerk of the House",
		"Name:", "Status:", "State/District:", "Amount", "Gains >"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// ParsePTR extracts the transactions from a PTR PDF.
func ParsePTR(data []byte) ([]Txn, error) {
	lines, err := pdfLines(data)
	if err != nil {
		return nil, err
	}
	var out []Txn
	var cur *Txn
	var amount, asset string
	inDesc := false
	flush := func() {
		if cur == nil {
			return
		}
		cur.Asset = strings.Join(strings.Fields(asset), " ")
		if m := tickerRe.FindStringSubmatch(cur.Asset); m != nil {
			cur.Ticker, cur.AssetType = m[1], m[2]
		} else if m := typeRe.FindStringSubmatch(cur.Asset); m != nil {
			cur.AssetType = m[1]
		}
		ms := moneyRe.FindAllStringSubmatch(amount, -1)
		if len(ms) > 0 {
			cur.Low, _ = strconv.ParseInt(strings.ReplaceAll(ms[0][1], ",", ""), 10, 64)
			cur.High = cur.Low
			if len(ms) > 1 {
				cur.High, _ = strconv.ParseInt(strings.ReplaceAll(ms[1][1], ",", ""), 10, 64)
			}
		}
		cur.Desc = strings.TrimSpace(cur.Desc)
		out = append(out, *cur)
		cur = nil
	}
	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "* For the complete list") || strings.HasPrefix(l, "I CERTIFY") {
			break
		}
		if m := rowRe.FindStringSubmatch(l); m != nil {
			flush()
			cur = &Txn{Owner: m[1], Type: m[3], Date: m[4]}
			asset, amount, inDesc = m[2], m[6], false
			continue
		}
		if cur == nil || isHeader(l) {
			continue
		}
		if statusRe.MatchString(l) {
			inDesc = false
			continue
		}
		if m := descRe.FindStringSubmatch(l); m != nil {
			cur.Desc, inDesc = m[1], true
			continue
		}
		if inDesc {
			cur.Desc += " " + l
			continue
		}
		// Continuation of the asset name, often ending in the rest of the amount range.
		if m := tailAmt.FindStringSubmatchIndex(l); m != nil {
			amount += " " + l[m[2]:m[3]]
			l = l[:m[0]]
		}
		asset += " " + l
	}
	flush()
	return out, nil
}
