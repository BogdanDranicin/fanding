package journal

import (
	"testing"
	"time"
)

func TestDayStart(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want string // expected YYYY-MM-DD of the MSK calendar day
	}{
		{
			name: "morning MSK stays same day",
			in:   time.Date(2026, 7, 10, 5, 0, 0, 0, time.UTC), // 08:00 MSK
			want: "2026-07-10",
		},
		{
			name: "just after MSK midnight rolls to next day",
			in:   time.Date(2026, 7, 10, 21, 30, 0, 0, time.UTC), // 00:30 MSK, 11 Jul
			want: "2026-07-11",
		},
		{
			name: "just before MSK midnight stays same day",
			in:   time.Date(2026, 7, 10, 20, 30, 0, 0, time.UTC), // 23:30 MSK, 10 Jul
			want: "2026-07-10",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dayStart(c.in)
			if got.Format("2006-01-02") != c.want {
				t.Errorf("dayStart(%s) = %s, want %s", c.in.Format(time.RFC3339), got.Format("2006-01-02"), c.want)
			}
			if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 {
				t.Errorf("dayStart not at midnight: %v", got)
			}
			if got.Location() != time.UTC {
				t.Errorf("dayStart location = %v, want UTC", got.Location())
			}
		})
	}
}
