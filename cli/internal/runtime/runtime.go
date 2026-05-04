// Package runtime computes how long a project's VM was actually running
// during a given time window, by replaying handoff/return events from the
// audit log.
//
// In Moorpost's local-first model, the VM is stopped by default and
// transitioned to running only by `moorpost handoff`, then back to stopped
// by `moorpost return`. So pairing handoff/return events approximates VM
// up-time. This is used to scale the list-price estimate from "calendar
// hours in period" (very wrong for handoff workflows) to "actual hours
// the VM was running" (closer to truth).
//
// This is NOT real billing data — it's still an estimate, but a good one.
// Real billed amounts ship in v1.1 behind a `--actual` opt-in flag.
package runtime

import (
	"sort"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/audit"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
)

// RunningHours computes the total hours the VM was running within period,
// by pairing audit-log handoff entries with subsequent return entries.
//
// Algorithm:
//   - Sort entries by timestamp (defensive — audit.Read already sorts but
//     callers may pass synthesized data).
//   - Walk entries; track an "open" handoff. When a return is seen, close
//     the interval and add (end-start) to the total, clamped to period.
//   - Orphan returns (no prior open handoff) are ignored.
//   - A trailing open handoff (VM still running) closes at min(period.End, now).
//   - Out-of-order or zero/negative intervals contribute 0.
//
// The function is pure given (entries, period, now) and is safe for
// repeated calls with the same arguments.
func RunningHours(entries []audit.Entry, period provider.TimeRange, now time.Time) float64 {
	if !period.End.After(period.Start) {
		return 0
	}
	sorted := make([]audit.Entry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})

	var total time.Duration
	var openAt time.Time
	open := false

	for _, e := range sorted {
		switch e.Command {
		case "handoff":
			if !open {
				openAt = e.Timestamp
				open = true
			}
			// If already open (e.g. handoff with no intervening return), keep
			// the earlier openAt — best-effort, since duplicate handoffs are
			// not expected in normal flow.
		case "return":
			if open {
				total += clampedInterval(openAt, e.Timestamp, period)
				open = false
			}
			// Orphan return — ignored.
		}
	}

	if open {
		// Trailing handoff with no return: VM is currently running. Close at
		// min(period.End, now) so we don't extrapolate past the requested
		// window or past wall-clock.
		end := period.End
		if now.Before(end) {
			end = now
		}
		total += clampedInterval(openAt, end, period)
	}

	return total.Hours()
}

// clampedInterval returns the portion of [start,end) that falls inside
// [period.Start, period.End). Returns 0 for inverted or empty intervals.
func clampedInterval(start, end time.Time, period provider.TimeRange) time.Duration {
	if !end.After(start) {
		return 0
	}
	if start.Before(period.Start) {
		start = period.Start
	}
	if end.After(period.End) {
		end = period.End
	}
	if !end.After(start) {
		return 0
	}
	return end.Sub(start)
}
