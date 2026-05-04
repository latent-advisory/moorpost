package runtime

import (
	"math"
	"testing"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/audit"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tt
}

func entry(cmd string, ts time.Time) audit.Entry {
	return audit.Entry{Command: cmd, Timestamp: ts}
}

// almostEqual avoids fragile float comparisons (sub-microsecond noise).
func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestRunningHours(t *testing.T) {
	periodStart := mustParse(t, "2026-05-01T00:00:00Z")
	periodEnd := mustParse(t, "2026-06-01T00:00:00Z")
	period := provider.TimeRange{Start: periodStart, End: periodEnd}
	now := mustParse(t, "2026-05-15T12:00:00Z") // mid-period

	cases := []struct {
		name    string
		entries []audit.Entry
		now     time.Time
		want    float64
	}{
		{
			name:    "empty",
			entries: nil,
			now:     now,
			want:    0,
		},
		{
			name: "single paired handoff/return inside period",
			entries: []audit.Entry{
				entry("handoff", mustParse(t, "2026-05-10T10:00:00Z")),
				entry("return", mustParse(t, "2026-05-10T11:30:00Z")),
			},
			now:  now,
			want: 1.5,
		},
		{
			name: "multiple cycles inside period",
			entries: []audit.Entry{
				entry("handoff", mustParse(t, "2026-05-02T09:00:00Z")),
				entry("return", mustParse(t, "2026-05-02T11:00:00Z")), // 2h
				entry("handoff", mustParse(t, "2026-05-08T14:00:00Z")),
				entry("return", mustParse(t, "2026-05-08T17:00:00Z")), // 3h
			},
			now:  now,
			want: 5,
		},
		{
			name: "trailing open handoff: clamped to now",
			entries: []audit.Entry{
				entry("handoff", mustParse(t, "2026-05-15T10:00:00Z")),
			},
			now:  now, // 12:00:00Z → 2h
			want: 2,
		},
		{
			name: "trailing open handoff, now past period.End: clamped to period.End",
			entries: []audit.Entry{
				entry("handoff", mustParse(t, "2026-05-31T22:00:00Z")),
			},
			now:  mustParse(t, "2026-06-15T00:00:00Z"), // 2 weeks past period
			want: 2,                                    // 22:00 → 24:00 = 2h
		},
		{
			name: "handoff before period start, return inside: only inside-period counted",
			entries: []audit.Entry{
				entry("handoff", mustParse(t, "2026-04-30T22:00:00Z")), // 2h before period
				entry("return", mustParse(t, "2026-05-01T01:00:00Z")),  // 1h into period
			},
			now:  now,
			want: 1, // [00:00, 01:00) = 1h
		},
		{
			name: "handoff inside, return after period end: capped at period.End",
			entries: []audit.Entry{
				entry("handoff", mustParse(t, "2026-05-31T23:00:00Z")), // 1h before end
				entry("return", mustParse(t, "2026-06-01T05:00:00Z")),  // 5h after end
			},
			now:  now,
			want: 1, // 23:00 → 24:00 = 1h
		},
		{
			name: "interval entirely before period: 0",
			entries: []audit.Entry{
				entry("handoff", mustParse(t, "2026-04-15T10:00:00Z")),
				entry("return", mustParse(t, "2026-04-15T12:00:00Z")),
			},
			now:  now,
			want: 0,
		},
		{
			name: "interval entirely after period: 0",
			entries: []audit.Entry{
				entry("handoff", mustParse(t, "2026-07-01T10:00:00Z")),
				entry("return", mustParse(t, "2026-07-01T12:00:00Z")),
			},
			now:  now,
			want: 0,
		},
		{
			name: "orphan return: ignored",
			entries: []audit.Entry{
				entry("return", mustParse(t, "2026-05-10T11:00:00Z")),
				entry("handoff", mustParse(t, "2026-05-15T10:00:00Z")),
				entry("return", mustParse(t, "2026-05-15T12:00:00Z")), // 2h
			},
			now:  now,
			want: 2,
		},
		{
			name: "duplicate handoff without intervening return: keeps earlier open",
			entries: []audit.Entry{
				entry("handoff", mustParse(t, "2026-05-10T10:00:00Z")),
				entry("handoff", mustParse(t, "2026-05-10T10:30:00Z")),
				entry("return", mustParse(t, "2026-05-10T12:00:00Z")), // 2h from earlier
			},
			now:  now,
			want: 2,
		},
		{
			name: "handoff/return with same timestamp: 0 contribution",
			entries: []audit.Entry{
				entry("handoff", mustParse(t, "2026-05-10T10:00:00Z")),
				entry("return", mustParse(t, "2026-05-10T10:00:00Z")),
			},
			now:  now,
			want: 0,
		},
		{
			name: "out-of-order entries: sorted before processing",
			entries: []audit.Entry{
				entry("return", mustParse(t, "2026-05-10T11:30:00Z")),
				entry("handoff", mustParse(t, "2026-05-10T10:00:00Z")),
			},
			now:  now,
			want: 1.5,
		},
		{
			name: "non-handoff/return entries: ignored",
			entries: []audit.Entry{
				entry("status", mustParse(t, "2026-05-10T09:00:00Z")),
				entry("handoff", mustParse(t, "2026-05-10T10:00:00Z")),
				entry("audit", mustParse(t, "2026-05-10T10:30:00Z")),
				entry("return", mustParse(t, "2026-05-10T11:00:00Z")), // 1h
				entry("cost", mustParse(t, "2026-05-10T11:30:00Z")),
			},
			now:  now,
			want: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RunningHours(tc.entries, period, tc.now)
			if !almostEqual(got, tc.want) {
				t.Errorf("RunningHours = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunningHours_InvertedPeriod(t *testing.T) {
	// Defensive: period.End before period.Start returns 0 without panicking.
	period := provider.TimeRange{
		Start: mustParse(t, "2026-06-01T00:00:00Z"),
		End:   mustParse(t, "2026-05-01T00:00:00Z"),
	}
	got := RunningHours([]audit.Entry{
		entry("handoff", mustParse(t, "2026-05-10T10:00:00Z")),
		entry("return", mustParse(t, "2026-05-10T11:00:00Z")),
	}, period, time.Now())
	if got != 0 {
		t.Errorf("inverted period: got %v, want 0", got)
	}
}

func TestRunningHours_ZeroLengthPeriod(t *testing.T) {
	t1 := mustParse(t, "2026-05-10T10:00:00Z")
	period := provider.TimeRange{Start: t1, End: t1}
	got := RunningHours([]audit.Entry{
		entry("handoff", t1.Add(-time.Hour)),
		entry("return", t1.Add(time.Hour)),
	}, period, time.Now())
	if got != 0 {
		t.Errorf("zero-length period: got %v, want 0", got)
	}
}

func TestRunningHours_PureFunction(t *testing.T) {
	// Same inputs → same output, idempotent and side-effect-free.
	period := provider.TimeRange{
		Start: mustParse(t, "2026-05-01T00:00:00Z"),
		End:   mustParse(t, "2026-06-01T00:00:00Z"),
	}
	now := mustParse(t, "2026-05-15T12:00:00Z")
	entries := []audit.Entry{
		entry("handoff", mustParse(t, "2026-05-10T10:00:00Z")),
		entry("return", mustParse(t, "2026-05-10T11:30:00Z")),
	}
	a := RunningHours(entries, period, now)
	b := RunningHours(entries, period, now)
	c := RunningHours(entries, period, now)
	if a != b || b != c {
		t.Errorf("not pure: got %v, %v, %v", a, b, c)
	}
	// Caller's slice unmodified.
	if entries[0].Command != "handoff" || entries[1].Command != "return" {
		t.Errorf("RunningHours mutated caller's slice: %v", entries)
	}
}
