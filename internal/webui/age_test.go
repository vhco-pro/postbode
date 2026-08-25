package webui

import (
	"testing"
	"time"
)

func TestHumanAge(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-2 * time.Hour, "just now"},
		{30 * time.Second, "just now"},
		{time.Minute, "1 minute ago"},
		{45 * time.Minute, "45 minutes ago"},
		{time.Hour, "1 hour ago"},
		{23 * time.Hour, "23 hours ago"},
		{day, "1 day ago"},
		{13 * day, "13 days ago"},
		{59 * day, "59 days ago"},
		{60 * day, "2 months ago"},
		{400 * day, "13 months ago"},
	}
	for _, c := range cases {
		if got := humanAge(c.d); got != c.want {
			t.Errorf("humanAge(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestSortByReceivedPutsOldestFirstAndUnknownLast(t *testing.T) {
	base := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	items := []itemView{
		{Subject: "newer", Received: base.Add(48 * time.Hour)},
		{Subject: "unknown"},
		{Subject: "older", Received: base},
	}

	sortByReceived(items)

	want := []string{"older", "newer", "unknown"}
	for i, w := range want {
		if items[i].Subject != w {
			t.Fatalf("order = [%s %s %s], want %v",
				items[0].Subject, items[1].Subject, items[2].Subject, want)
		}
	}
}
