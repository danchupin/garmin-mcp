package personal

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"
)

var setsStart = time.Date(2026, 11, 22, 0, 10, 0, 0, time.UTC)

type syntheticSet struct {
	kind        typedef.SetType
	categories  []typedef.ExerciseCategory
	repetitions uint16
	seconds     int
}

func syntheticFIT(t *testing.T, sets []syntheticSet) []byte {
	t.Helper()
	messages := []proto.Message{
		mesgdef.NewFileId(nil).SetType(typedef.FileActivity).SetManufacturer(typedef.ManufacturerGarmin).SetTimeCreated(setsStart).ToMesg(nil),
	}
	at := setsStart
	for i, set := range sets {
		length := time.Duration(set.seconds) * time.Second
		for second := 0; second <= set.seconds; second += 5 {
			bpm := uint8(90 + i*10 + second/5)
			messages = append(messages, mesgdef.NewRecord(nil).SetTimestamp(at.Add(time.Duration(second)*time.Second)).SetHeartRate(bpm).ToMesg(nil))
		}
		item := mesgdef.NewSet(nil).SetSetType(set.kind).SetStartTime(at).SetTimestamp(at.Add(length)).SetDurationScaled(float64(set.seconds)).SetMessageIndex(typedef.MessageIndex(i))
		if set.kind == typedef.SetTypeActive {
			item.SetCategory(set.categories).SetRepetitions(set.repetitions).SetWeightScaled(0)
		}
		messages = append(messages, item.ToMesg(nil))
		at = at.Add(length)
	}
	var buffer bytes.Buffer
	if err := encoder.New(&buffer).Encode(&proto.FIT{Messages: messages}); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func zipped(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for name, body := range files {
		w, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func executeSets(t *testing.T, body []byte, contentType string) (map[string]any, error) {
	t.Helper()
	client := newClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/download-service/files/activity/25158438760" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return response(200, string(body), contentType), nil
	}, nil)
	result, err := client.Execute(context.Background(), "activity_sets", map[string]any{"activityId": "25158438760"})
	if err != nil {
		return nil, err
	}
	if result.Kind != "json" || result.Bytes != nil {
		t.Fatalf("unexpected result kind %q", result.Kind)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload %T", result.Data)
	}
	return data, nil
}

func numberOf(t *testing.T, value any) float64 {
	t.Helper()
	n, ok := value.(json.Number)
	if !ok {
		t.Fatalf("expected json.Number, got %T (%v)", value, value)
	}
	f, err := n.Float64()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestActivitySetsDecodesFITSetsFromZip(t *testing.T) {
	fit := syntheticFIT(t, []syntheticSet{
		{typedef.SetTypeActive, []typedef.ExerciseCategory{typedef.ExerciseCategoryPullUp}, 14, 50},
		{typedef.SetTypeRest, nil, 0, 240},
		{typedef.SetTypeActive, []typedef.ExerciseCategory{typedef.ExerciseCategoryPullUp}, 14, 52},
		{typedef.SetTypeRest, nil, 0, 260},
		{typedef.SetTypeActive, []typedef.ExerciseCategory{typedef.ExerciseCategoryPullUp}, 10, 45},
	})
	data, err := executeSets(t, zipped(t, map[string][]byte{"25158438760_ACTIVITY.fit": fit, "readme.txt": []byte("ignored")}), "application/zip")
	if err != nil {
		t.Fatal(err)
	}
	if data["source"] != "original_fit" || numberOf(t, data["activeSets"]) != 3 || numberOf(t, data["totalRepetitions"]) != 38 {
		t.Fatalf("unexpected summary %v", data)
	}
	sets := data["sets"].([]any)
	if len(sets) != 5 {
		t.Fatalf("expected 5 sets, got %d", len(sets))
	}
	first := sets[0].(map[string]any)
	if first["type"] != "active" || numberOf(t, first["activeNumber"]) != 1 || numberOf(t, first["repetitions"]) != 14 || numberOf(t, first["durationSeconds"]) != 50 {
		t.Fatalf("unexpected first set %v", first)
	}
	if categories := first["categories"].([]any); len(categories) != 1 || categories[0] != "pull_up" {
		t.Fatalf("unexpected categories %v", first["categories"])
	}
	if first["startTime"] != "2026-11-22T00:10:00Z" || first["endTime"] != "2026-11-22T00:10:50Z" {
		t.Fatalf("unexpected set window %v", first)
	}
	if numberOf(t, first["averageHR"]) != 95 || numberOf(t, first["maxHR"]) != 100 {
		t.Fatalf("unexpected heart rate %v", first)
	}
	rest := sets[1].(map[string]any)
	if rest["type"] != "rest" || rest["repetitions"] != nil || rest["activeNumber"] != nil || numberOf(t, rest["durationSeconds"]) != 240 {
		t.Fatalf("unexpected rest %v", rest)
	}
	last := sets[4].(map[string]any)
	if numberOf(t, last["activeNumber"]) != 3 || numberOf(t, last["repetitions"]) != 10 || numberOf(t, last["index"]) != 5 {
		t.Fatalf("unexpected last set %v", last)
	}
}

// Watches that do not recognise the exercise write [unknown, curl, bench_press] with
// invalid subtypes; the trailing slots are padding, not real categories.
func TestActivitySetsOmitsUnrecognisedExerciseCategories(t *testing.T) {
	fit := syntheticFIT(t, []syntheticSet{
		{typedef.SetTypeActive, []typedef.ExerciseCategory{typedef.ExerciseCategoryUnknown, typedef.ExerciseCategoryCurl, typedef.ExerciseCategoryBenchPress}, 15, 30},
	})
	data, err := executeSets(t, fit, "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	set := data["sets"].([]any)[0].(map[string]any)
	if _, ok := set["categories"]; ok {
		t.Fatalf("expected no categories for an unrecognised exercise, got %v", set["categories"])
	}
	if numberOf(t, set["repetitions"]) != 15 {
		t.Fatalf("unexpected set %v", set)
	}
}

func TestActivitySetsAcceptsBareFITAndActivitiesWithoutSets(t *testing.T) {
	data, err := executeSets(t, syntheticFIT(t, nil), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if sets := data["sets"].([]any); len(sets) != 0 || numberOf(t, data["activeSets"]) != 0 {
		t.Fatalf("unexpected payload %v", data)
	}
}

func TestActivitySetsRejectsMissingOrCorruptFIT(t *testing.T) {
	if _, err := executeSets(t, zipped(t, map[string][]byte{"notes.txt": []byte("x")}), "application/zip"); codeOf(err) != "GARMIN_NOT_FOUND" {
		t.Fatalf("expected GARMIN_NOT_FOUND, got %v", err)
	}
	if _, err := executeSets(t, zipped(t, map[string][]byte{"a.fit": []byte("not a fit file")}), "application/zip"); codeOf(err) != "GARMIN_INVALID_RESPONSE" {
		t.Fatalf("expected GARMIN_INVALID_RESPONSE, got %v", err)
	}
	if _, err := executeSets(t, []byte("PK\x03\x04broken"), "application/zip"); codeOf(err) != "GARMIN_INVALID_RESPONSE" {
		t.Fatalf("expected GARMIN_INVALID_RESPONSE, got %v", err)
	}
}

func TestActivitySetsReusesDocumentedFileEndpoint(t *testing.T) {
	op, err := GetOperation("activity_sets")
	if err != nil {
		t.Fatal(err)
	}
	file, _ := GetOperation("activity_file")
	if op.Path != file.Path || op.Scope != "activities:export" || op.Binary || op.Derived != derivedFITSets {
		t.Fatalf("unexpected operation %+v", op)
	}
	for _, documented := range DocumentedOperations() {
		if documented.ID == "activity_sets" {
			t.Fatal("derived operation listed as documented")
		}
	}
}
