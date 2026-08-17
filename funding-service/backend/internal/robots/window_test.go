package robots

import (
	"math/rand"
	"testing"
	"time"
)

// Скан не должен заглядывать вперёд собственной точки отсчёта: находка, целиком
// лежащая после now, — это не «сейчас работающий робот», а будущее ленты.
func TestScanIgnoresPrintsAfterNow(t *testing.T) {
	rnd := rand.New(rand.NewSource(9))
	d := NewDetector(DefaultConfig())
	d.Add(robotPrints("SBER", SideBuy, base, 10, 60, 400, 0, rnd)...)

	// Точка отсчёта — до начала серии: видеть нечего.
	if found := d.Scan(base.Add(-time.Minute)); len(found) != 0 {
		t.Errorf("скан до начала серии нашёл %d роботов: %+v", len(found), found)
	}

	// Точка отсчёта в середине серии: робот виден, но его последний принт не может
	// оказаться позже now.
	at := base.Add(5 * time.Minute)
	found := d.Scan(at)
	if len(found) != 1 {
		t.Fatalf("найдено %d роботов, хотим 1", len(found))
	}
	if found[0].LastSeen.After(at) {
		t.Errorf("LastSeen = %s, позже конца окна %s", found[0].LastSeen, at)
	}
}

// Принты, пришедшие с меткой чуть впереди точки скана, не теряются: их подхватит
// следующий скан.
func TestPrintsAheadOfNowSurviveForNextScan(t *testing.T) {
	rnd := rand.New(rand.NewSource(10))
	d := NewDetector(DefaultConfig())
	d.Add(robotPrints("GAZP", SideSell, base, 8, 40, 150, 0, rnd)...)

	mid := base.Add(2 * time.Minute)
	d.Scan(mid)
	if n := d.TapeLen("GAZP"); n != 40 {
		t.Errorf("после скана в середине серии в ленте %d принтов, хотим все 40", n)
	}
	if found := d.Scan(base.Add(10 * time.Minute)); len(found) != 1 {
		t.Errorf("следующий скан нашёл %d роботов, хотим 1", len(found))
	}
}
