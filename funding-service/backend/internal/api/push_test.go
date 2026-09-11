package api

import (
	"strings"
	"testing"
	"time"
)

// Очередь наполняет браузер, а его часы могут быть сбиты, расписание — испорчено,
// а намерения — недобрыми. Поэтому принятое расписание чистится, а не кладётся
// в базу как есть.
func TestWakeupsFromFiltersJunk(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return now.Add(d).UnixMilli() }

	got := wakeupsFrom([]pushWakeupJSON{
		{At: at(-time.Hour), Title: "вчерашний"},
		{At: at(30 * time.Hour), Title: "послезавтрашний"},
		{At: at(-30 * time.Second), Title: "чуть раньше часов сервера"},
		{At: at(time.Minute), Title: "  Каждый час  ", Body: "13:00 МСК", Tag: "time-alarm:час"},
		{At: at(2 * time.Minute), Title: "   "},
		{At: at(3 * time.Minute), Title: strings.Repeat("я", maxTitleLen+40)},
	}, now)

	if len(got) != 3 {
		t.Fatalf("принято %d отметок, ждали 3: %+v", len(got), got)
	}
	if got[1].Title != "Каждый час" {
		t.Errorf("заголовок %q — пробелы по краям должны уйти", got[1].Title)
	}
	if got[1].Tag != "time-alarm:час" {
		t.Errorf("тег %q", got[1].Tag)
	}
	if n := len([]rune(got[2].Title)); n != maxTitleLen {
		t.Errorf("длинный заголовок обрезан до %d рун, ждали %d", n, maxTitleLen)
	}
}

// Больше maxWakeups отметок за раз не берём: очередь на сутки вперёд никому не
// нужна, а забить её ничего не стоит.
func TestWakeupsFromCaps(t *testing.T) {
	now := time.Now()
	list := make([]pushWakeupJSON, maxWakeups+20)
	for i := range list {
		list[i] = pushWakeupJSON{At: now.Add(time.Duration(i+1) * time.Minute).UnixMilli(), Title: "сигнал"}
	}
	if got := wakeupsFrom(list, now); len(got) != maxWakeups {
		t.Fatalf("принято %d, ждали потолок %d", len(got), maxWakeups)
	}
}

// Сервис ходит по адресу подписки сам, поэтому адрес обязан быть чужим
// https-адресом: иначе в него можно было бы подсунуть внутренний адрес сети.
func TestValidEndpoint(t *testing.T) {
	ok := []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/abc",
	}
	for _, e := range ok {
		if !validEndpoint(e) {
			t.Errorf("адрес %q отвергнут, а он нормальный", e)
		}
	}
	bad := []string{
		"", "не адрес", "http://fcm.googleapis.com/x", "file:///etc/passwd",
		"https://", "https://x/" + strings.Repeat("a", maxEndpointLen),
	}
	for _, e := range bad {
		if validEndpoint(e) {
			t.Errorf("адрес %q принят, а не должен", e)
		}
	}
}
