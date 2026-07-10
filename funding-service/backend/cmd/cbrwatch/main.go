// Command cbrwatch is an unattended latency tracker for CBR rate publications.
//
// It polls every CBR channel (official XML, SOAP, mirrors) once per second during
// the 16:00–19:00 MSK publication window (and every 5 minutes otherwise), and
// records the first moment each channel shows a fresh rate date. When a brand-new
// date appears it also snapshots our own service (GET /api/v1/snapshot) to capture
// the predicted CBR rate and compare it against the freshly published actual.
//
// Output (under -logdir, default = the directory above the executable):
//   - cbrwatch-YYYY-MM-DD.jsonl : raw machine-readable events (one JSON per line)
//   - cbrwatch-summary.md       : human/agent-readable per-publication breakdown
//
// This binary is meant to run continuously via a Windows Scheduled Task; it sleeps
// cheaply outside the window. See docs/cbr-latency-tracking.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/funding-service/backend/internal/source/cbr"
)

var msk = time.FixedZone("MSK", 3*60*60)

func main() {
	var (
		once        = flag.Bool("once", false, "poll all channels once, print results, and exit")
		logDir      = flag.String("logdir", "", "directory for log files (default: dir above the executable)")
		snapshotURL = flag.String("snapshot-url", "http://localhost:8080/api/v1/snapshot", "our service snapshot endpoint for prediction capture")
		windowIv    = flag.Duration("window-interval", time.Second, "poll interval inside the 16:00–19:00 MSK window")
		idleIv      = flag.Duration("idle-interval", 5*time.Minute, "poll interval outside the window")
	)
	flag.Parse()

	client := cbr.DirectClient()
	channels := cbr.AllChannels()

	if *once {
		runOnce(client, channels)
		return
	}

	dir := resolveLogDir(*logDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cbrwatch: cannot create logdir %s: %v\n", dir, err)
		os.Exit(1)
	}

	w := &watcher{
		dir:         dir,
		snapshotURL: *snapshotURL,
		client:      client,
		channels:    channels,
		days:        make(map[string]*pubDay),
	}
	w.loadRecent()
	w.writeSummary()
	fmt.Printf("cbrwatch: logging to %s (window %v / idle %v)\n", dir, *windowIv, *idleIv)

	for {
		iv := *idleIv
		if inWindow(time.Now().In(msk)) {
			iv = *windowIv
		}
		w.poll()
		time.Sleep(iv)
	}
}

// inWindow reports whether t (MSK) is inside the CBR publication window 16:00–19:00.
func inWindow(t time.Time) bool {
	h := t.Hour()
	return h >= 16 && h < 19
}

// runOnce polls all channels in parallel and prints a compact table to stdout.
func runOnce(client *http.Client, channels []cbr.Channel) {
	type row struct {
		ch  cbr.Channel
		res cbr.ChannelResult
		dur time.Duration
		err error
	}
	rows := make([]row, len(channels))
	var wg sync.WaitGroup
	for i, ch := range channels {
		wg.Add(1)
		go func(i int, ch cbr.Channel) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			start := time.Now()
			res, err := ch.Fetch(ctx, client)
			rows[i] = row{ch, res, time.Since(start), err}
		}(i, ch)
	}
	wg.Wait()
	sort.Slice(rows, func(i, j int) bool { return rows[i].dur < rows[j].dur })
	fmt.Printf("%-18s %-12s %-10s %-10s %8s  %s\n", "channel", "rate_date", "usd", "eur", "ms", "err")
	for _, r := range rows {
		errStr := ""
		if r.err != nil {
			errStr = r.err.Error()
		}
		fmt.Printf("%-18s %-12s %-10.4f %-10.4f %8d  %s\n",
			r.ch.ID, r.res.Date, r.res.USD, r.res.EUR, r.dur.Milliseconds(), errStr)
	}
}

// ─── watcher state ────────────────────────────────────────────────────────────

type chanSeen struct {
	at  time.Time // first time this channel showed the date (UTC)
	usd float64
	eur float64
	cny float64
}

// pubDay accumulates, for one rate date, the first-seen time per channel plus the
// predicted-vs-actual comparison captured when the date first appeared.
type pubDay struct {
	date     string
	channels map[string]chanSeen
	actUSD   float64
	actEUR   float64
	predUSD  float64
	predEUR  float64
	predDone bool
}

type watcher struct {
	dir         string
	snapshotURL string
	client      *http.Client
	channels    []cbr.Channel
	days        map[string]*pubDay
}

func (w *watcher) poll() {
	results := make([]cbr.ChannelResult, len(w.channels))
	ids := make([]string, len(w.channels))
	var wg sync.WaitGroup
	for i, ch := range w.channels {
		wg.Add(1)
		go func(i int, ch cbr.Channel) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			res, err := ch.Fetch(ctx, w.client)
			if err == nil {
				results[i] = res
			}
			ids[i] = ch.ID
		}(i, ch)
	}
	wg.Wait()

	changed := false
	for i, res := range results {
		if res.Date == "" {
			continue
		}
		if w.recordFirstSeen(ids[i], res) {
			changed = true
		}
	}
	if changed {
		w.writeSummary()
	}
}

// recordFirstSeen logs the first observation of (channel, date). Returns true if
// this was a new observation (i.e. something to re-summarise).
func (w *watcher) recordFirstSeen(chID string, res cbr.ChannelResult) bool {
	now := time.Now().UTC()
	d := w.days[res.Date]
	if d == nil {
		d = &pubDay{date: res.Date, channels: make(map[string]chanSeen), actUSD: res.USD, actEUR: res.EUR}
		w.days[res.Date] = d
		// Capture our prediction only for a genuine next-day publication (date in the
		// future MSK), not for the already-current baseline date seen at startup.
		n := time.Now().In(msk)
		todayCivil := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
		if parseDate(res.Date).After(todayCivil) {
			w.capturePrediction(d)
		}
	}
	if _, ok := d.channels[chID]; ok {
		return false // already recorded this channel for this date
	}
	d.channels[chID] = chanSeen{at: now, usd: res.USD, eur: res.EUR, cny: res.CNY}
	if d.actUSD == 0 {
		d.actUSD, d.actEUR = res.USD, res.EUR
	}
	w.appendJSONL(map[string]any{
		"ts_utc":    now.Format(time.RFC3339Nano),
		"ts_msk":    now.In(msk).Format("15:04:05.000"),
		"channel":   chID,
		"rate_date": res.Date,
		"usd":       res.USD,
		"eur":       res.EUR,
		"cny":       res.CNY,
		"event":     "first_seen",
	})
	return true
}

// capturePrediction fetches our service snapshot and stores predicted USD/EUR for
// comparison with the actual published rate. Silently no-ops if unreachable.
func (w *watcher) capturePrediction(d *pubDay) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.snapshotURL, nil)
	if err != nil {
		return
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var snap struct {
		USDRUBF struct {
			PredictedCBRate float64 `json:"predicted_cb_rate"`
		} `json:"USDRUBF"`
		EURRUBF struct {
			PredictedCBRate float64 `json:"predicted_cb_rate"`
		} `json:"EURRUBF"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return
	}
	d.predUSD = snap.USDRUBF.PredictedCBRate
	d.predEUR = snap.EURRUBF.PredictedCBRate
	d.predDone = true
	now := time.Now().UTC()
	w.appendJSONL(map[string]any{
		"ts_utc":    now.Format(time.RFC3339Nano),
		"ts_msk":    now.In(msk).Format("15:04:05.000"),
		"rate_date": d.date,
		"pred_usd":  d.predUSD,
		"pred_eur":  d.predEUR,
		"event":     "prediction",
	})
}

// ─── persistence ──────────────────────────────────────────────────────────────

func (w *watcher) jsonlPath(t time.Time) string {
	return filepath.Join(w.dir, "cbrwatch-"+t.In(msk).Format("2006-01-02")+".jsonl")
}

func (w *watcher) appendJSONL(event map[string]any) {
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	f, err := os.OpenFile(w.jsonlPath(time.Now()), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// loadRecent replays today's and yesterday's JSONL so a restart keeps prior state.
func (w *watcher) loadRecent() {
	now := time.Now()
	for _, day := range []time.Time{now.AddDate(0, 0, -1), now} {
		w.loadFile(w.jsonlPath(day))
	}
}

func (w *watcher) loadFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		date, _ := ev["rate_date"].(string)
		if date == "" {
			continue
		}
		d := w.days[date]
		if d == nil {
			d = &pubDay{date: date, channels: make(map[string]chanSeen)}
			w.days[date] = d
		}
		switch ev["event"] {
		case "first_seen":
			chID, _ := ev["channel"].(string)
			tsStr, _ := ev["ts_utc"].(string)
			at, _ := time.Parse(time.RFC3339Nano, tsStr)
			usd, _ := ev["usd"].(float64)
			eur, _ := ev["eur"].(float64)
			cny, _ := ev["cny"].(float64)
			if _, ok := d.channels[chID]; !ok {
				d.channels[chID] = chanSeen{at: at, usd: usd, eur: eur, cny: cny}
			}
			if d.actUSD == 0 {
				d.actUSD, d.actEUR = usd, eur
			}
		case "prediction":
			d.predUSD, _ = ev["pred_usd"].(float64)
			d.predEUR, _ = ev["pred_eur"].(float64)
			d.predDone = true
		}
	}
}

// ─── summary rendering ────────────────────────────────────────────────────────

func (w *watcher) writeSummary() {
	var b strings.Builder
	b.WriteString("# cbrwatch — гонка каналов ЦБ\n\n")
	b.WriteString("_Обновлено: " + time.Now().In(msk).Format("2006-01-02 15:04:05") + " МСК._\n")
	b.WriteString("_Сырые события: cbrwatch-YYYY-MM-DD.jsonl. Колонка `+Nс` — отставание канала от лидера публикации._\n\n")

	dates := make([]string, 0, len(w.days))
	for date := range w.days {
		dates = append(dates, date)
	}
	// Sort by parsed date descending (newest first); unpar. dates go last.
	sort.Slice(dates, func(i, j int) bool {
		return parseDate(dates[i]).After(parseDate(dates[j]))
	})
	if len(dates) > 15 {
		dates = dates[:15]
	}

	for _, date := range dates {
		w.renderDay(&b, w.days[date])
	}

	_ = os.WriteFile(filepath.Join(w.dir, "cbrwatch-summary.md"), []byte(b.String()), 0o644)
}

func (w *watcher) renderDay(b *strings.Builder, d *pubDay) {
	type seen struct {
		id string
		at time.Time
	}
	list := make([]seen, 0, len(d.channels))
	for id, cs := range d.channels {
		list = append(list, seen{id, cs.at})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })

	fmt.Fprintf(b, "## Публикация %s\n\n", d.date)
	if len(list) == 0 {
		b.WriteString("_нет наблюдений_\n\n")
		return
	}
	leader := list[0].at
	fmt.Fprintf(b, "Первым показал курс: **%s** в %s МСК (USD=%.4f, EUR=%.4f)\n\n",
		list[0].id, leader.In(msk).Format("15:04:05.000"), d.actUSD, d.actEUR)
	b.WriteString("| канал | время МСК | отставание |\n|---|---|---|\n")
	for _, s := range list {
		delta := s.at.Sub(leader).Seconds()
		note := ""
		if s.id == "cbr_official_xml" {
			note = " ← наш боевой канал"
		}
		fmt.Fprintf(b, "| %s%s | %s | +%.1f с |\n",
			s.id, note, s.at.In(msk).Format("15:04:05.000"), delta)
	}
	b.WriteString("\n")

	if d.predDone && (d.predUSD > 0 || d.predEUR > 0) {
		b.WriteString("Прогноз vs факт:\n\n")
		renderPred(b, "USD", d.predUSD, d.actUSD)
		renderPred(b, "EUR", d.predEUR, d.actEUR)
		b.WriteString("\n")
	} else {
		b.WriteString("_Прогноз не захвачен (наш сервис был недоступен в момент публикации)._\n\n")
	}
}

func renderPred(b *strings.Builder, label string, pred, act float64) {
	if pred <= 0 || act <= 0 {
		return
	}
	errAbs := pred - act
	errPct := errAbs / act * 100
	fmt.Fprintf(b, "- %s: прогноз %.4f, факт %.4f, ошибка %+.4f (%+.3f%%)\n", label, pred, act, errAbs, errPct)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func parseDate(s string) time.Time {
	t, err := time.Parse("02.01.2006", s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func resolveLogDir(override string) string {
	if override != "" {
		return override
	}
	exe, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exe)
		// Built to .../logs/bin/cbrwatch.exe → logs is the parent of bin.
		if strings.EqualFold(filepath.Base(exeDir), "bin") {
			return filepath.Dir(exeDir)
		}
		return filepath.Join(exeDir, "logs")
	}
	return "logs"
}
