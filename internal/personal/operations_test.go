package personal

import (
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestRegistryCoversOfficialSnapshot(t *testing.T) {
	data, err := os.ReadFile("../../docs/personal-api-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Operations []struct {
			Method, Path string
			Names        []string `json:"parameter_names"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Operations) != 26 || len(Operations()) != 26 {
		t.Fatal("expected all 26 documented GET operations")
	}
	byPath := map[string]Operation{}
	for _, op := range Operations() {
		byPath[op.Path] = op
	}
	for _, expected := range snapshot.Operations {
		op, ok := byPath[expected.Path]
		if !ok || expected.Method != "GET" {
			t.Fatalf("missing operation %s", expected.Path)
		}
		sort.Strings(expected.Names)
		if strings.Join(op.ParameterNames(), ",") != strings.Join(expected.Names, ",") {
			t.Fatalf("parameter mismatch for %s: %v / %v", op.ID, op.ParameterNames(), expected.Names)
		}
		if op.InputSchema["additionalProperties"] != false {
			t.Fatal("registry must reject unknown parameters")
		}
	}
}

func TestEveryOperationBuildsItsDocumentedPath(t *testing.T) {
	for _, op := range Operations() {
		args := map[string]any{}
		for name, p := range op.parameters {
			if !p.required {
				continue
			}
			if p.kind == "identifier" {
				args[name] = "18446744073709551615"
			} else {
				args[name] = "2026-10-20"
			}
		}
		path, err := op.Build(args)
		if err != nil {
			t.Fatalf("%s: %v", op.ID, err)
		}
		u, err := url.Parse(path)
		if err != nil {
			t.Fatal(err)
		}
		expected := strings.ReplaceAll(op.Path, "{activityId}", "18446744073709551615")
		for _, name := range []string{"calendarDate", "startDate", "endDate", "fromDate", "toDate", "untilDate", "date"} {
			expected = strings.ReplaceAll(expected, "{"+name+"}", "2026-10-20")
		}
		if u.Path != expected {
			t.Fatalf("%s: %s != %s", op.ID, u.Path, expected)
		}
	}
}

func TestActivityDefaultsFiltersAndRepeatedParameters(t *testing.T) {
	op, _ := GetOperation("activities")
	path, err := op.Build(map[string]any{"sortOrder": "AsC", "search": "run & rest", "requiredField": []any{"gps", "power"}, "excludedActivitySubType": []string{"mountain_biking"}})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(path)
	q := u.Query()
	for name, expected := range map[string]string{"start": "0", "limit": "25", "favorite": "-1", "excludeChildren": "true", "sortOrder": "AsC", "search": "run & rest"} {
		if q.Get(name) != expected {
			t.Fatalf("%s: %q", name, q.Get(name))
		}
	}
	if !reflect.DeepEqual(q["requiredField"], []string{"gps", "power"}) {
		t.Fatal(q)
	}
	if strings.Contains(path, "run & rest") {
		t.Fatal("query was not encoded")
	}
	meal, _ := GetOperation("nutrition")
	path, err = meal.Build(map[string]any{"date": "2026-10-20", "mealId": json.Number("9223372036854775807")})
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(path)
	if u.Query().Get("mealId") != "9223372036854775807" || u.Query().Get("includeMedia") != "false" {
		t.Fatal(path)
	}
}

func TestValidationFailsClosed(t *testing.T) {
	for _, item := range []struct {
		operation string
		args      map[string]any
	}{
		{"profile", map[string]any{"authorization": "secret"}},
		{"activity", map[string]any{"activityId": "../profile"}},
		{"activity", map[string]any{"activityId": float64(1e20)}},
		{"activity", map[string]any{"activityId": json.Number("1e20")}},
		{"sleep_daily", map[string]any{"startDate": "2026-02-30", "endDate": "2026-03-01"}},
		{"sleep_daily", map[string]any{"startDate": "2026-10-02", "endDate": "2026-10-01"}},
		{"steps", map[string]any{"startDate": "2026-10-01", "endDate": "2026-10-29"}},
		{"activities", map[string]any{"upperLatitude": 12}},
		{"activities", map[string]any{"minDistance": 20, "maxDistance": 10}},
		{"activities", map[string]any{"limit": 101}},
		{"activities", map[string]any{"start": -1}},
		{"activities", map[string]any{"startDate": "2026-10-02", "endDate": "2026-10-01"}},
		{"activities", map[string]any{"search": "bad\nvalue"}},
		{"activities", map[string]any{"sortOrder": "ascending"}},
		{"body_battery", map[string]any{"startDate": "2026-10-01", "endDate": "2026-10-01", "chunks": 1441}},
		{"nutrition", map[string]any{"date": "2026-10-20", "includeMedia": "true"}},
		{"profile", map[string]any{"user_id": "victim"}},
	} {
		op, err := GetOperation(item.operation)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := op.Build(item.args); err == nil {
			t.Errorf("accepted invalid %s %v", item.operation, item.args)
		}
	}
	if _, err := GetOperation("arbitrary-url"); err == nil {
		t.Fatal("accepted unknown operation")
	}
}

func TestDocumentedInclusiveWindowLimits(t *testing.T) {
	for _, item := range []struct{ id, first, last string }{
		{"steps", "2026-10-01", "2026-10-28"}, {"stress", "2026-10-01", "2026-10-28"},
		{"sleep_daily", "2026-10-01", "2026-10-31"}, {"hydration", "2026-10-01", "2026-10-31"},
		{"body_battery", "2026-10-01", "2026-10-31"}, {"daily_summaries", "2024-01-01", "2024-12-31"},
	} {
		op, _ := GetOperation(item.id)
		if _, err := op.Build(map[string]any{"startDate": item.first, "endDate": item.last}); err != nil {
			t.Fatalf("%s valid maximum: %v", item.id, err)
		}
	}
}
