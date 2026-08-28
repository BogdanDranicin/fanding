package robots

import (
	"bufio"
	"compress/gzip"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Проверка детектора на настоящей ленте вместо синтетики.
//
// В testdata лежит получасовой кусок ленты MOEX за 28.08.2026 (17:03–17:33 МСК)
// по шести инструментам, снятый с публичного фида ISS. Роботы в нём известны:
// в тот же момент их независимо показывал платный скринер (moex.plutos.finance),
// и его ответы записаны в wantRobots. Это единственный тест, который ловит провалы
// рекола: на синтетике детектор всегда выглядел хорошо, а на живой ленте до склейки
// сделок в приказы находил одного робота из пятнадцати.
//
// Лента ISS пишет время с точностью до секунды, поэтому самых быстрых роботов
// (такт 3 с и меньше) в ней не различить в принципе — таких в эталоне нет.
func TestDetectorOnRealTape(t *testing.T) {
	type want struct {
		symbol string
		side   Side
		qty    float64 // лотовка робота
		period float64 // такт, секунды
	}
	// Эталон: роботы, которых платный скринер показывал на этой ленте.
	wantRobots := []want{
		{"AFLT", SideSell, 57, 30},
		{"GAZP", SideSell, 372, 30},
		{"HEAD", SideBuy, 7, 16},
		{"PLZL", SideSell, 413, 30},
		{"T", SideSell, 2741, 60},
		{"TATNP", SideSell, 19, 32},
	}

	msk := time.FixedZone("MSK", 3*60*60)
	day := time.Date(2026, 8, 28, 0, 0, 0, 0, msk)

	cfg := DefaultConfig()
	cfg.Window = 30 * time.Minute
	d := NewDetector(cfg)

	files, err := filepath.Glob(filepath.Join("testdata", "tape-2026-08-28", "*.csv.gz"))
	if err != nil || len(files) == 0 {
		t.Fatalf("лента не найдена: %v", err)
	}
	for _, path := range files {
		symbol := strings.TrimSuffix(filepath.Base(path), ".csv.gz")
		prints, err := readTapeFixture(path, symbol, day)
		if err != nil {
			t.Fatalf("%s: %v", symbol, err)
		}
		d.Add(prints...)
	}

	found := d.Scan(day.Add(17*time.Hour + 33*time.Minute))

	for _, w := range wantRobots {
		var hit *Robot
		for i := range found {
			r := &found[i]
			if r.Symbol != w.symbol || r.Side != w.side {
				continue
			}
			if w.qty < r.QtyMin-1 || w.qty > r.QtyMax+1 {
				continue
			}
			// Такт сверяем с допуском в 5 %: скринер округляет период до секунды.
			if math.Abs(r.PeriodSec-w.period) > 0.05*w.period {
				continue
			}
			hit = r
			break
		}
		if hit == nil {
			t.Errorf("%s %s: робот %.0f л с тактом %.0f с не найден", w.symbol, w.side, w.qty, w.period)
			continue
		}
		if hit.Provisional {
			t.Errorf("%s: робот на получасе ленты помечен предварительным", w.symbol)
		}
		// Занятость сетки: у настоящего робота такты заняты плотно.
		if occ := occupancy(*hit); occ < 0.6 {
			t.Errorf("%s: занятость тактов %.2f, хотим не ниже 0.6", w.symbol, occ)
		}
	}

	// Один и тот же робот не должен показываться дважды под разными таймингами.
	for i := range found {
		for j := i + 1; j < len(found); j++ {
			if harmonic(found[i], found[j]) || SameRobot(found[i], found[j]) {
				t.Errorf("двойник: %s %.0f л такт %.2f и такт %.2f",
					found[i].Symbol, found[i].QtyTypical, found[i].PeriodSec, found[j].PeriodSec)
			}
		}
	}
}

// readTapeFixture читает сжатую ленту: строка на сделку, «HH:MM:SS,цена,лоты,B|S».
func readTapeFixture(path, symbol string, day time.Time) ([]Print, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	var out []Print
	sc := bufio.NewScanner(gz)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ",")
		if len(fields) < 4 {
			continue
		}
		hms := strings.Split(fields[0], ":")
		if len(hms) != 3 {
			continue
		}
		h, _ := strconv.Atoi(hms[0])
		m, _ := strconv.Atoi(hms[1])
		s, _ := strconv.Atoi(hms[2])
		price, _ := strconv.ParseFloat(fields[1], 64)
		qty, _ := strconv.ParseFloat(fields[2], 64)
		side := Side(strings.TrimSpace(fields[3]))
		if side != SideBuy && side != SideSell {
			continue
		}
		out = append(out, Print{
			Symbol: symbol,
			Time: day.Add(time.Duration(h)*time.Hour +
				time.Duration(m)*time.Minute + time.Duration(s)*time.Second),
			Price: price,
			Qty:   qty,
			Side:  side,
		})
	}
	return out, sc.Err()
}
