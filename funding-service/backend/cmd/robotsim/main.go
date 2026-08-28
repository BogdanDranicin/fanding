// Command robotsim прогоняет сохранённую ленту сделок через детектор роботов и
// печатает, кого он в ней видит. Нужен, чтобы сверять находки с эталоном на
// настоящей ленте, а не только на синтетике тестов: рекол детектора виден лишь
// на живом рынке, где рядом с роботом идёт чужой поток того же размера.
//
// Лента — CSV на инструмент, по строке на сделку:
//
//	TRADENO,HH:MM:SS,цена,лоты,B|S
//
// Готовит их scripts/capture_tape.py из публичного фида ISS.
//
// Запуск:
//
//	go run ./cmd/robotsim -dir tapes -at 17:41 -window 20m
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/funding-service/backend/internal/robots"
)

func main() {
	dir := flag.String("dir", "tapes", "каталог с CSV-лентами")
	at := flag.String("at", "17:41", "момент среза, МСК HH:MM[:SS]")
	window := flag.Duration("window", 20*time.Minute, "окно анализа")
	only := flag.String("symbols", "", "разбирать только эти тикеры через запятую")
	tolAbs := flag.Duration("tol", 0, "допуск на такт, абсолютный; 0 — как в DefaultConfig")
	sigma := flag.Float64("sigma", -1, "порог неслучайности; <0 — как в DefaultConfig")
	occ := flag.Float64("occupancy", -1, "минимальная занятость тактов; <0 — как в DefaultConfig")
	minPrints := flag.Int("minprints", 0, "минимум принтов в серии; 0 — как в DefaultConfig")
	flag.Parse()

	msk := time.FixedZone("MSK", 3*60*60)
	day := time.Date(2000, 1, 1, 0, 0, 0, 0, msk) // дата не важна: лента одна и та же

	cfg := robots.DefaultConfig()
	cfg.Window = *window
	if *tolAbs > 0 {
		cfg.PeriodTolAbs = *tolAbs
	}
	if *sigma >= 0 {
		cfg.NoiseSigma = *sigma
	}
	if *occ >= 0 {
		cfg.MinOccupancy = *occ
	}
	if *minPrints > 0 {
		cfg.MinPrints = *minPrints
		cfg.MinBeats = *minPrints - 1
	}
	det := robots.NewDetector(cfg)

	want := map[string]bool{}
	for _, s := range strings.Split(*only, ",") {
		if s = strings.TrimSpace(s); s != "" {
			want[strings.ToUpper(s)] = true
		}
	}

	files, err := filepath.Glob(filepath.Join(*dir, "*.csv"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "лент не найдено в %s\n", *dir)
		os.Exit(1)
	}
	sort.Strings(files)

	cut := parseClock(day, *at)
	total := 0
	for _, f := range files {
		sym := strings.TrimSuffix(filepath.Base(f), ".csv")
		if len(want) > 0 && !want[sym] {
			continue
		}
		prints, err := readTape(f, sym, day)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", sym, err)
			continue
		}
		det.Add(prints...)
		total += len(prints)
	}
	fmt.Printf("лента: %d принтов, срез %s, окно %s\n\n", total, *at, *window)

	t0 := time.Now()
	found := det.Scan(cut)
	elapsed := time.Since(t0)
	bySym := map[string][]robots.Robot{}
	for _, r := range found {
		bySym[r.Symbol] = append(bySym[r.Symbol], r)
	}
	syms := make([]string, 0, len(bySym))
	for s := range bySym {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, s := range syms {
		rs := bySym[s]
		sort.Slice(rs, func(i, j int) bool { return rs[i].Confidence > rs[j].Confidence })
		fmt.Printf("%-7s", s)
		for i, r := range rs {
			if i > 0 {
				fmt.Printf("%-7s", "")
			}
			fmt.Printf("  %s %7.2f–%-7.2f л  такт %7.2f с  принтов %3d  тактов %3d/%-3d  разброс %.3f  уверенность %.2f  %s–%s\n",
				r.Side, r.QtyMin, r.QtyMax, r.PeriodSec, r.Prints, r.Hits, r.Beats, r.Jitter, r.Confidence,
				r.FirstSeen.Format("15:04:05"), r.LastSeen.Format("15:04:05"))
		}
	}
	fmt.Printf("\nвсего роботов: %d по %d инструментам, скан занял %s\n", len(found), len(bySym), elapsed)
}

// parseClock — «17:41» или «17:41:30» в момент внутри дня ленты.
func parseClock(day time.Time, s string) time.Time {
	parts := strings.Split(s, ":")
	get := func(i int) int {
		if i >= len(parts) {
			return 0
		}
		v, _ := strconv.Atoi(parts[i])
		return v
	}
	return day.Add(time.Duration(get(0))*time.Hour +
		time.Duration(get(1))*time.Minute + time.Duration(get(2))*time.Second)
}

func readTape(path, symbol string, day time.Time) ([]robots.Print, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []robots.Print
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ",")
		if len(fields) < 5 {
			continue
		}
		no, _ := strconv.ParseInt(fields[0], 10, 64)
		price, _ := strconv.ParseFloat(fields[2], 64)
		qty, _ := strconv.ParseFloat(fields[3], 64)
		side := robots.Side(strings.TrimSpace(fields[4]))
		if side != robots.SideBuy && side != robots.SideSell {
			continue
		}
		out = append(out, robots.Print{
			TradeNo: no,
			Symbol:  symbol,
			Time:    parseClock(day, fields[1]),
			Price:   price,
			Qty:     qty,
			Side:    side,
		})
	}
	return out, sc.Err()
}
