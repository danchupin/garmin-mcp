package personal

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/kit/datetime"
	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"

	"github.com/danchupin/garmin-mcp/internal/domain"
)

// derivedFITSets marks an operation that downloads the original activity file
// and returns the FIT set messages as JSON instead of the binary archive.
const derivedFITSets = "fit_sets"

const (
	maxFITEntries   = 16
	recordHeartRate = 3 // record.heart_rate field number in the FIT profile
)

type fitSet struct {
	Index            int      `json:"index"`
	ActiveNumber     int      `json:"activeNumber,omitempty"`
	Type             string   `json:"type"`
	Categories       []string `json:"categories,omitempty"`
	CategorySubtypes []int    `json:"categorySubtypes,omitempty"`
	Repetitions      *int     `json:"repetitions,omitempty"`
	WeightKg         *float64 `json:"weightKg,omitempty"`
	DurationSeconds  *float64 `json:"durationSeconds,omitempty"`
	StartTime        string   `json:"startTime,omitempty"`
	EndTime          string   `json:"endTime,omitempty"`
	AverageHR        *int     `json:"averageHR,omitempty"`
	MaxHR            *int     `json:"maxHR,omitempty"`
}

type fitSets struct {
	Source           string   `json:"source"`
	ActiveSets       int      `json:"activeSets"`
	TotalRepetitions int      `json:"totalRepetitions"`
	Sets             []fitSet `json:"sets"`
}

type heartRateSample struct {
	at  uint32
	bpm uint8
}

func noFITFile() error {
	return domain.NewError("GARMIN_NOT_FOUND", "No FIT file is available for this activity.", 404)
}

func tooLarge() error {
	return domain.NewError("RESPONSE_TOO_LARGE", "Garmin response exceeds the operation size limit.", 502)
}

// fitPayloads returns the FIT files contained in a Garmin original-file download:
// either a ZIP archive with one or more .fit entries, or a bare FIT file.
func fitPayloads(data []byte) ([][]byte, error) {
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return [][]byte{data}, nil
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, invalidResponse()
	}
	var payloads [][]byte
	var total int64
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() || !strings.EqualFold(path.Ext(entry.Name), ".fit") {
			continue
		}
		if len(payloads) >= maxFITEntries {
			return nil, invalidResponse()
		}
		if entry.UncompressedSize64 > uint64(fileBytes) {
			return nil, tooLarge()
		}
		reader, err := entry.Open()
		if err != nil {
			return nil, invalidResponse()
		}
		body, err := io.ReadAll(io.LimitReader(reader, fileBytes-total+1))
		reader.Close()
		if err != nil {
			return nil, invalidResponse()
		}
		total += int64(len(body))
		if total > fileBytes {
			return nil, tooLarge()
		}
		payloads = append(payloads, body)
	}
	if len(payloads) == 0 {
		return nil, noFITFile()
	}
	return payloads, nil
}

func roundTo(value float64, places int) float64 {
	scale := math.Pow(10, float64(places))
	return math.Round(value*scale) / scale
}

func categoryName(category typedef.ExerciseCategory) string {
	name := category.String()
	if strings.HasPrefix(name, "ExerciseCategoryInvalid") {
		return "unknown_" + strconv.Itoa(int(category))
	}
	return name
}

func setTypeName(setType typedef.SetType) string {
	switch setType {
	case typedef.SetTypeActive:
		return "active"
	case typedef.SetTypeRest:
		return "rest"
	}
	return "unknown"
}

func formatFITTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// heartRate summarizes record samples in [start, end] (FIT timestamps, seconds).
func heartRate(samples []heartRateSample, start, end uint32) (*int, *int) {
	first := sort.Search(len(samples), func(i int) bool { return samples[i].at >= start })
	var sum, count, maximum int
	for _, sample := range samples[first:] {
		if sample.at > end {
			break
		}
		sum += int(sample.bpm)
		count++
		maximum = max(maximum, int(sample.bpm))
	}
	if count == 0 {
		return nil, nil
	}
	average := int(math.Round(float64(sum) / float64(count)))
	return &average, &maximum
}

// recognisedExercise reports whether the watch identified the exercise. Garmin writes the
// primary category in the first slot; when it is unknown or invalid, the remaining slots
// are padding (observed: [unknown, curl, bench_press] with invalid subtypes).
func recognisedExercise(categories []typedef.ExerciseCategory) bool {
	return len(categories) > 0 && categories[0] != typedef.ExerciseCategoryInvalid && categories[0] != typedef.ExerciseCategoryUnknown
}

func exerciseCategories(set *mesgdef.Set) ([]string, []int) {
	var names []string
	var subtypes []int
	for _, category := range set.Category {
		if category != typedef.ExerciseCategoryInvalid && category != typedef.ExerciseCategoryUnknown {
			names = append(names, categoryName(category))
		}
	}
	for _, subtype := range set.CategorySubtype {
		if subtype != basetype.Uint16Invalid {
			subtypes = append(subtypes, int(subtype))
		}
	}
	return names, subtypes
}

func toFITSet(set *mesgdef.Set, samples []heartRateSample) fitSet {
	output := fitSet{Type: setTypeName(set.SetType)}
	if recognisedExercise(set.Category) {
		output.Categories, output.CategorySubtypes = exerciseCategories(set)
	}
	if set.Repetitions != basetype.Uint16Invalid {
		repetitions := int(set.Repetitions)
		output.Repetitions = &repetitions
	}
	if set.Weight != basetype.Uint16Invalid {
		weight := roundTo(set.WeightScaled(), 2)
		output.WeightKg = &weight
	}
	start, end := set.StartTime, time.Time{}
	if set.Duration != basetype.Uint32Invalid {
		duration := roundTo(set.DurationScaled(), 3)
		output.DurationSeconds = &duration
		length := time.Duration(set.Duration) * time.Millisecond
		if start.IsZero() && !set.Timestamp.IsZero() {
			start = set.Timestamp.Add(-length)
		}
		if !start.IsZero() {
			end = start.Add(length)
		}
	} else if !start.IsZero() && !set.Timestamp.IsZero() {
		end = set.Timestamp
	}
	output.StartTime, output.EndTime = formatFITTime(start), formatFITTime(end)
	if !start.IsZero() && !end.IsZero() {
		output.AverageHR, output.MaxHR = heartRate(samples, datetime.ToUint32(start), datetime.ToUint32(end))
	}
	return output
}

// decodeFITSets extracts set messages (strength training sets and rests) from a
// Garmin original activity download. Missing FIT values stay missing.
func decodeFITSets(data []byte) (any, error) {
	payloads, err := fitPayloads(data)
	if err != nil {
		return nil, err
	}
	var sets []*mesgdef.Set
	var samples []heartRateSample
	for _, payload := range payloads {
		dec := decoder.New(bytes.NewReader(payload))
		for dec.Next() {
			file, err := dec.Decode()
			if err != nil {
				return nil, invalidResponse()
			}
			for i := range file.Messages {
				message := &file.Messages[i]
				switch message.Num {
				case typedef.MesgNumSet:
					sets = append(sets, mesgdef.NewSet(message))
				case typedef.MesgNumRecord:
					at := message.FieldValueByNum(proto.FieldNumTimestamp).Uint32()
					bpm := message.FieldValueByNum(recordHeartRate).Uint8()
					if at != basetype.Uint32Invalid && bpm != basetype.Uint8Invalid && bpm > 0 {
						samples = append(samples, heartRateSample{at: at, bpm: bpm})
					}
				}
			}
		}
	}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].at < samples[j].at })
	result := fitSets{Source: "original_fit", Sets: make([]fitSet, 0, len(sets))}
	for i, set := range sets {
		item := toFITSet(set, samples)
		item.Index = i + 1
		if item.Type == "active" {
			result.ActiveSets++
			item.ActiveNumber = result.ActiveSets
			if item.Repetitions != nil {
				result.TotalRepetitions += *item.Repetitions
			}
		}
		result.Sets = append(result.Sets, item)
	}
	// Round-trip through JSON so the payload has the same shape as Garmin JSON responses.
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, invalidResponse()
	}
	jsonDecoder := json.NewDecoder(bytes.NewReader(encoded))
	jsonDecoder.UseNumber()
	var payload any
	if err := jsonDecoder.Decode(&payload); err != nil {
		return nil, invalidResponse()
	}
	return payload, nil
}
