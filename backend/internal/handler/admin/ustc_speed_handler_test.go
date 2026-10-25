package admin

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseUSTCSpeedQueryDefaultsToTodayInShanghai(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 30, 0, 0, time.UTC)
	query, err := parseUSTCSpeedQueryValues(url.Values{}, now)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	location, err := time.LoadLocation(ustcSpeedDefaultTimezone)
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, 10, 26, 0, 0, 0, 0, location)
	wantEnd := time.Date(2026, 10, 27, 0, 0, 0, 0, location)
	if !query.Start.Equal(wantStart) || !query.End.Equal(wantEnd) {
		t.Fatalf("unexpected half-open range: got [%s, %s), want [%s, %s)", query.Start, query.End, wantStart, wantEnd)
	}
	if query.Source != "real" || query.Metric != "generation_tps" || query.Interval != "auto" || query.GroupBy != "model" || query.Aggregate != "mean" {
		t.Fatalf("unexpected defaults: source=%q metric=%q interval=%q group_by=%q aggregate=%q", query.Source, query.Metric, query.Interval, query.GroupBy, query.Aggregate)
	}
	if query.Reasoning != "all" || query.Stream != "all" || query.Limit != ustcSpeedDefaultLimit || query.Offset != 0 {
		t.Fatalf("unexpected filter defaults: reasoning=%q stream=%q limit=%d offset=%d", query.Reasoning, query.Stream, query.Limit, query.Offset)
	}
}

func TestParseUSTCSpeedQueryFilters(t *testing.T) {
	values := url.Values{
		"start_date":  {"2026-10-01"},
		"end_date":    {"2026-10-03"},
		"timezone":    {"America/Los_Angeles"},
		"model":       {"model-a"},
		"source":      {"all"},
		"account_ids": {"9, 4,9"},
		"task_id":     {"12"},
		"reasoning":   {"unknown"},
		"stream":      {"false"},
		"status":      {"recorded"},
		"profile":     {"legacy"},
		"group_by":    {"profile"},
		"input_min":   {"10"},
		"input_max":   {"20"},
		"output_min":  {"3"},
		"output_max":  {"7"},
		"metric":      {"ttft_ms"},
		"interval":    {"1m"},
		"aggregate":   {"mean"},
		"limit":       {"100"},
		"offset":      {"50"},
	}
	query, err := parseUSTCSpeedQueryValues(values, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	location, _ := time.LoadLocation("America/Los_Angeles")
	if want := time.Date(2026, 10, 1, 0, 0, 0, 0, location); !query.Start.Equal(want) {
		t.Errorf("start = %s, want %s", query.Start, want)
	}
	if want := time.Date(2026, 10, 4, 0, 0, 0, 0, location); !query.End.Equal(want) {
		t.Errorf("end = %s, want %s", query.End, want)
	}
	if query.Model != "model-a" || query.Source != "all" || query.Status != "recorded" || query.Profile != "legacy" {
		t.Errorf("unexpected identity filters: %+v", query)
	}
	if len(query.AccountIDs) != 2 || query.AccountIDs[0] != 9 || query.AccountIDs[1] != 4 {
		t.Errorf("account IDs were not parsed and deduplicated: %v", query.AccountIDs)
	}
	if query.TaskID != 12 || query.Reasoning != "unknown" || query.Stream != "false" {
		t.Errorf("unexpected task/protocol filters: %+v", query)
	}
	if query.InputMin == nil || *query.InputMin != 10 || query.InputMax == nil || *query.InputMax != 20 || query.OutputMin == nil || *query.OutputMin != 3 || query.OutputMax == nil || *query.OutputMax != 7 {
		t.Errorf("unexpected token ranges: %+v", query)
	}
	if query.Metric != "ttft_ms" || query.Interval != "1m" || query.GroupBy != "profile" || query.Aggregate != "mean" || query.Limit != 100 || query.Offset != 50 {
		t.Errorf("unexpected metric/paging filters: %+v", query)
	}
	if query.Timezone != "America/Los_Angeles" {
		t.Errorf("timezone must reach the repository for local-hour grouping: %q", query.Timezone)
	}
	if query.Hourly {
		t.Error("hourly should default to off")
	}
}

func TestParseUSTCSpeedQueryHourly(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE"} {
		values := url.Values{"hourly": {value}, "timezone": {"Asia/Shanghai"}}
		query, err := parseUSTCSpeedQueryValues(values, time.Now())
		if err != nil {
			t.Fatalf("hourly=%q should be accepted: %v", value, err)
		}
		if !query.Hourly || query.Timezone != "Asia/Shanghai" {
			t.Fatalf("unexpected hourly query: %+v", query)
		}
	}
	for _, value := range []string{"0", "false"} {
		query, err := parseUSTCSpeedQueryValues(url.Values{"hourly": {value}}, time.Now())
		if err != nil || query.Hourly {
			t.Fatalf("hourly=%q should disable grouping: %+v, %v", value, query, err)
		}
	}
	if _, err := parseUSTCSpeedQueryValues(url.Values{"hourly": {"yes"}}, time.Now()); err == nil {
		t.Fatal("ambiguous hourly values should be rejected")
	}
}

func TestParseUSTCSpeedQueryAccepts180CalendarDays(t *testing.T) {
	values := url.Values{"start_date": {"2026-01-01"}, "end_date": {"2026-06-29"}}
	if _, err := parseUSTCSpeedQueryValues(values, time.Now()); err != nil {
		t.Fatalf("180-day range should be accepted: %v", err)
	}
	values.Set("end_date", "2026-06-30")
	if _, err := parseUSTCSpeedQueryValues(values, time.Now()); err == nil {
		t.Fatal("181-day range should be rejected")
	}
}

func TestParseUSTCSpeedQueryRejectsInvalidFilters(t *testing.T) {
	tooManyIDs := make([]string, ustcSpeedMaxAccounts+1)
	for i := range tooManyIDs {
		tooManyIDs[i] = fmt.Sprint(i + 1)
	}
	tooManyIDsCSV := strings.Join(tooManyIDs, ",")
	tests := []struct {
		name   string
		values url.Values
	}{
		{name: "timezone", values: url.Values{"timezone": {"Mars/Olympus"}}},
		{name: "date format", values: url.Values{"start_date": {"2026/10/01"}}},
		{name: "date ordering", values: url.Values{"start_date": {"2026-10-02"}, "end_date": {"2026-10-01"}}},
		{name: "source", values: url.Values{"source": {"benchmark"}}},
		{name: "metric", values: url.Values{"metric": {"unknown"}}},
		{name: "interval", values: url.Values{"interval": {"2h"}}},
		{name: "aggregate", values: url.Values{"aggregate": {"median"}}},
		{name: "group_by", values: url.Values{"group_by": {"account"}}},
		{name: "status", values: url.Values{"status": {"pending"}}},
		{name: "reasoning", values: url.Values{"reasoning": {"maybe"}}},
		{name: "stream", values: url.Values{"stream": {"yes"}}},
		{name: "invalid account id", values: url.Values{"account_ids": {"1,-2"}}},
		{name: "too many account ids", values: url.Values{"account_ids": {tooManyIDsCSV}}},
		{name: "task id", values: url.Values{"task_id": {"0"}}},
		{name: "input lower exceeds upper", values: url.Values{"input_min": {"3"}, "input_max": {"2"}}},
		{name: "negative output range", values: url.Values{"output_min": {"-1"}}},
		{name: "limit", values: url.Values{"limit": {"201"}}},
		{name: "offset", values: url.Values{"offset": {"-1"}}},
		{name: "profile length", values: url.Values{"profile": {strings.Repeat("x", 129)}}},
		{name: "model length", values: url.Values{"model": {strings.Repeat("x", 201)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseUSTCSpeedQueryValues(tt.values, time.Now()); err == nil {
				t.Fatal("expected invalid query to be rejected")
			}
		})
	}
}
