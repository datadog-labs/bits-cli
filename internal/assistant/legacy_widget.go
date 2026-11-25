package assistant

import (
	"encoding/json"
	"math"
	"strconv"
	"time"
)

// LegacyWidget decodes both historical chart definitions and title-only tiles.
// Keep this reader while stored conversations still contain either format.
type LegacyWidget struct {
	Definition json.RawMessage
	Title      string
	time       *legacyWidgetTime
}

type legacyWidgetTime struct {
	Type     string  `json:"type"`
	From     float64 `json:"from"`
	To       float64 `json:"to"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit"`
	LiveSpan string  `json:"live_span"`
}

func decodeLegacyWidget(raw json.RawMessage, title string) *LegacyWidget {
	w := &LegacyWidget{Title: title}
	var def struct {
		Type  string            `json:"type"`
		Title string            `json:"title"`
		Time  *legacyWidgetTime `json:"time"`
	}
	if json.Unmarshal(raw, &def) == nil {
		if def.Title != "" {
			w.Title = def.Title
		}
		if def.Type != "" {
			w.Definition, w.time = raw, def.Time
		}
	}
	return w
}

// Chart resolves rolling windows at the time the chart is rendered or opened.
// Calendar-aligned and unknown time forms leave the renderer's default in place.
func (w *LegacyWidget) Chart(now time.Time) *Chart {
	if len(w.Definition) == 0 {
		return nil
	}
	chart := &Chart{Definition: w.Definition}
	if w.time == nil {
		return chart
	}
	if from, to, ok := fixedWidgetRange(w.time); ok {
		chart.Start, chart.End = time.UnixMilli(from), time.UnixMilli(to)
	} else if span, months, ok := liveWidgetSpan(w.time); ok {
		now = now.Truncate(time.Millisecond)
		chart.Start, chart.End = now.Add(-span), now
		if months != 0 {
			chart.Start = subtractWidgetMonths(now, months)
		}
	}
	return chart
}

func fixedWidgetRange(t *legacyWidgetTime) (from, to int64, ok bool) {
	if t.Type != "fixed" {
		return 0, 0, false
	}
	f, e := t.From, t.To
	if f < 0 || f >= e || e > 1<<53 || f != math.Trunc(f) || e != math.Trunc(e) {
		return 0, 0, false
	}
	return int64(f), int64(e), true
}

// Years use the web UI's 365-day approximation; months use calendar dates.
func liveWidgetSpan(t *legacyWidgetTime) (span time.Duration, months int, ok bool) {
	if t.LiveSpan != "" {
		return legacyWidgetSpan(t.LiveSpan)
	}
	if t.Type != "live" {
		return 0, 0, false
	}
	return widgetSpan(t.Value, t.Unit)
}

func legacyWidgetSpan(s string) (span time.Duration, months int, ok bool) {
	digits := 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	value, err := strconv.Atoi(s[:digits])
	if err != nil {
		return 0, 0, false
	}
	var unit string
	switch s[digits:] {
	case "m":
		unit = "minute"
	case "h":
		unit = "hour"
	case "d":
		unit = "day"
	case "w":
		unit = "week"
	case "mo":
		unit = "month"
	case "y":
		unit = "year"
	default:
		return 0, 0, false
	}
	return widgetSpan(float64(value), unit)
}

func widgetSpan(value float64, unit string) (span time.Duration, months int, ok bool) {
	if value <= 0 || value != math.Trunc(value) {
		return 0, 0, false
	}
	day := 24 * time.Hour
	var per time.Duration
	switch unit {
	case "minute":
		per = time.Minute
	case "hour":
		per = time.Hour
	case "day":
		per = day
	case "week":
		per = 7 * day
	case "month":
		per = 30 * day
	case "year":
		per = 365 * day
	default:
		return 0, 0, false
	}
	if value > float64(math.MaxInt64/int64(per)) {
		return 0, 0, false
	}
	if unit == "month" {
		return 0, int(value), true
	}
	return time.Duration(value) * per, 0, true
}

// subtractWidgetMonths clamps the day to the target month's last day, matching
// the web time selector (March 31 minus one month becomes February 28 or 29).
func subtractWidgetMonths(now time.Time, months int) time.Time {
	year, month, day := now.Date()
	target := time.Date(year, month, 1, 12, 0, 0, 0, now.Location()).AddDate(0, -months, 0)
	lastDay := time.Date(target.Year(), target.Month()+1, 0, 12, 0, 0, 0, now.Location()).Day()
	hour, minute, second := now.Clock()
	return time.Date(target.Year(), target.Month(), min(day, lastDay), hour, minute, second, now.Nanosecond(), now.Location())
}
