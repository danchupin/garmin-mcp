package personal

import (
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danchupin/garmin-mcp/internal/domain"
)

type Error = domain.Error

func invalidArguments() error {
	return domain.NewError("INVALID_ARGUMENTS", "The operation parameters are invalid.", 400)
}

type parameter struct {
	kind     string
	required bool
	value    any
	minimum  float64
	maximum  float64
}

// Operation is an allowlisted Garmin GET operation, with its exact input contract.
type Operation struct {
	ID            string         `json:"id"`
	Path          string         `json:"path"`
	Group         string         `json:"group"`
	Scope         string         `json:"scope"`
	Binary        bool           `json:"binary"`
	InputSchema   map[string]any `json:"input_schema"`
	MaxWindowDays int            `json:"max_window_days,omitempty"`
	parameters    map[string]parameter
	rangeStart    string
	rangeEnd      string
}

var digits = regexp.MustCompile(`^[0-9]{1,20}$`)
var placeholder = regexp.MustCompile(`\{([^}]+)\}`)

const maxSafeInteger = 9007199254740991

func required(kind string) parameter             { return parameter{kind: kind, required: true} }
func optional(kind string) parameter             { return parameter{kind: kind} }
func defaulted(kind string, value any) parameter { return parameter{kind: kind, value: value} }
func bounded(kind string, minimum, maximum float64, value any) parameter {
	return parameter{kind: kind, minimum: minimum, maximum: maximum, value: value}
}

func schemaFor(p parameter) map[string]any {
	schema := map[string]any{}
	switch p.kind {
	case "date":
		schema["type"], schema["format"] = "string", "date"
	case "text", "sort":
		schema["type"], schema["minLength"], schema["maxLength"] = "string", 1, 512
		if p.kind == "sort" {
			schema["pattern"] = "^(?:[aA][sS][cC]|[dD][eE][sS][cC])$"
		}
	case "identifier":
		schema["anyOf"] = []any{map[string]any{"type": "integer", "minimum": 0}, map[string]any{"type": "string", "pattern": "^[0-9]{1,20}$"}}
	case "integer":
		schema["type"], schema["minimum"], schema["maximum"] = "integer", p.minimum, float64(maxSafeInteger)
		if p.maximum != 0 {
			schema["maximum"] = p.maximum
		}
	case "number":
		schema["type"], schema["minimum"], schema["maximum"] = "number", p.minimum, p.maximum
	case "favorite":
		schema["type"], schema["enum"] = "integer", []int{-1, 0, 1}
	case "boolean":
		schema["type"] = "boolean"
	case "texts":
		schema["type"], schema["maxItems"] = "array", 20
		schema["items"] = schemaFor(optional("text"))
	}
	if p.value != nil {
		schema["default"] = p.value
	}
	return schema
}

func makeOperation(id, path, group string, fields map[string]parameter, start, end string, maximum int) Operation {
	op := Operation{ID: id, Path: path, Group: group, Scope: group + ":read", Binary: id == "activity_file", parameters: fields, rangeStart: start, rangeEnd: end, MaxWindowDays: maximum}
	if op.Binary {
		op.Scope = "activities:export"
	}
	properties := map[string]any{}
	var mandatory []string
	for name, p := range fields {
		properties[name] = schemaFor(p)
		if p.required {
			mandatory = append(mandatory, name)
		}
	}
	sort.Strings(mandatory)
	op.InputSchema = map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(mandatory) > 0 {
		op.InputSchema["required"] = mandatory
	}
	return op
}

func rangeOperation(id, path, group, start, end string, maximum int, extra map[string]parameter) Operation {
	fields := map[string]parameter{start: required("date"), end: required("date")}
	for name, p := range extra {
		fields[name] = p
	}
	return makeOperation(id, path, group, fields, start, end, maximum)
}

func activitySearchFields() map[string]parameter {
	fields := map[string]parameter{
		"start": defaulted("integer", 0), "limit": bounded("integer", 1, 100, 25), "favorite": defaulted("favorite", -1),
		"sortOrder": defaulted("sort", "desc"), "excludeChildren": defaulted("boolean", true),
		"startDate": optional("date"), "endDate": optional("date"), "decoDive": optional("boolean"),
		"upperLatitude": bounded("number", -90, 90, nil), "rightLongitude": bounded("number", -180, 180, nil),
		"lowerLatitude": bounded("number", -90, 90, nil), "leftLongitude": bounded("number", -180, 180, nil),
		"excludedActivitySubType": optional("texts"), "requiredField": optional("texts"),
	}
	for _, name := range []string{"sortBy", "search", "activityType", "activitySubType", "eventType"} {
		fields[name] = optional("text")
	}
	for _, name := range []string{"minDistance", "maxDistance", "minDuration", "maxDuration", "minElevation", "maxElevation", "lowerMaxDepth", "upperMaxDepth", "minElapsedDuration", "maxElapsedDuration", "minBottomTime", "maxBottomTime", "minAvgPower"} {
		fields[name] = optional("integer")
	}
	return fields
}

var operations = []Operation{
	makeOperation("activity", "/activity-service/activity/{activityId}", "activities", map[string]parameter{"activityId": required("identifier")}, "", "", 0),
	makeOperation("activity_workouts", "/activity-service/activity/{activityId}/workouts", "activities", map[string]parameter{"activityId": required("identifier")}, "", "", 0),
	makeOperation("activities", "/activitylist-service/activities/search/activities", "activities", activitySearchFields(), "", "", 0),
	makeOperation("heart_rate_zones", "/biometric-service/heartRateZones/v2", "profile", map[string]parameter{"generateDefaults": defaulted("boolean", true)}, "", "", 0),
	makeOperation("activity_file", "/download-service/files/activity/{activityId}", "activities", map[string]parameter{"activityId": required("identifier")}, "", "", 0),
	makeOperation("fitness_age", "/fitnessage-service/fitnessage/v2/{calendarDate}", "fitness_age", map[string]parameter{"calendarDate": required("date"), "fullHistoryScan": defaulted("boolean", true)}, "", "", 0),
	rangeOperation("weight_goals", "/goal-service/goal/user/effective/weightgoal/{startDate}/{endDate}", "body", "startDate", "endDate", 0, nil),
	makeOperation("hrv", "/hrv-service/hrv/{calendarDate}", "wellness", map[string]parameter{"calendarDate": required("date")}, "", "", 0),
	rangeOperation("hrv_daily", "/hrv-service/hrv/daily/{fromDate}/{toDate}", "wellness", "fromDate", "toDate", 0, nil),
	makeOperation("nutrition", "/nutrition-service/food/logs/{date}", "nutrition", map[string]parameter{"date": required("date"), "includeGoals": defaulted("boolean", true), "includeNutrition": defaulted("boolean", true), "includeMedia": defaulted("boolean", false), "mealId": optional("identifier")}, "", "", 0),
	makeOperation("menstrual_summary", "/periodichealth-service/menstrualcycle/summary/{calendarDate}", "womens_health", map[string]parameter{"calendarDate": required("date")}, "", "", 0),
	makeOperation("menstrual_day", "/periodichealth-service/menstrualcycle/dayview/{calendarDate}", "womens_health", map[string]parameter{"calendarDate": required("date"), "todayDate": optional("date"), "reducedResponse": defaulted("boolean", false), "skipbm": defaulted("boolean", false), "includeAnalysis": defaulted("boolean", false)}, "", "", 0),
	makeOperation("sleep", "/sleep-service/sleep/dailySleep", "wellness", map[string]parameter{"date": optional("date")}, "", "", 0),
	rangeOperation("sleep_daily", "/sleep-service/sleep/dailySleepSummariesByDate", "wellness", "startDate", "endDate", 31, nil),
	makeOperation("profile", "/userprofile-service/userprofile/settings", "profile", map[string]parameter{}, "", "", 0),
	rangeOperation("hydration", "/usersummary-service/stats/hydration/daily/{startDate}/{endDate}", "wellness", "startDate", "endDate", 31, nil),
	rangeOperation("steps", "/usersummary-service/stats/steps/daily/{startDate}/{endDate}", "wellness", "startDate", "endDate", 28, nil),
	rangeOperation("stress", "/usersummary-service/stats/stress/daily/{startDate}/{endDate}", "wellness", "startDate", "endDate", 28, nil),
	makeOperation("daily_summary", "/usersummary-service/usersummary/daily", "wellness", map[string]parameter{"calendarDate": optional("date")}, "", "", 0),
	rangeOperation("daily_summaries", "/usersummary-service/v2/dailysummaries", "wellness", "startDate", "endDate", 366, nil),
	rangeOperation("weights", "/weight-service/weight/all/{startDate}/{endDate}", "body", "startDate", "endDate", 0, nil),
	rangeOperation("weight_range", "/weight-service/weight/range/{startDate}/{endDate}", "body", "startDate", "endDate", 0, map[string]parameter{"includeAll": defaulted("boolean", false)}),
	rangeOperation("body_battery", "/wellness-service/wellness/bodyBattery/reports/daily", "wellness", "startDate", "endDate", 31, map[string]parameter{"chunks": bounded("integer", 1, 1440, nil)}),
	makeOperation("spo2", "/wellness-service/wellness/daily/spo2acclimation/{calendarDate}", "wellness", map[string]parameter{"calendarDate": required("date")}, "", "", 0),
	makeOperation("heart_rate", "/wellness-service/wellness/dailyHeartRate", "wellness", map[string]parameter{"date": optional("date")}, "", "", 0),
	rangeOperation("wellness_activities", "/wellnessactivity-service/activity/summary/range/{fromDate}/{untilDate}", "wellness", "fromDate", "untilDate", 0, nil),
}

func Operations() []Operation { return append([]Operation(nil), operations...) }

func GetOperation(id string) (Operation, error) {
	for _, op := range operations {
		if op.ID == id {
			return op, nil
		}
	}
	return Operation{}, domain.NewError("UNKNOWN_OPERATION", "The Garmin operation is not supported.", 400)
}

func Scopes() []string {
	seen := map[string]bool{}
	var result []string
	for _, op := range operations {
		if !seen[op.Scope] {
			seen[op.Scope] = true
			result = append(result, op.Scope)
		}
	}
	sort.Strings(result)
	return result
}

func number(value any) (float64, bool) {
	var n float64
	switch v := value.(type) {
	case float64:
		n = v
	case float32:
		n = float64(v)
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case int32:
		n = float64(v)
	case uint:
		n = float64(v)
	case uint64:
		n = float64(v)
	case json.Number:
		var err error
		n, err = v.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, !math.IsInf(n, 0) && !math.IsNaN(n)
}

func normalizeParameter(p parameter, value any) (any, bool) {
	switch p.kind {
	case "date":
		v, ok := value.(string)
		if !ok || len(v) != 10 {
			return nil, false
		}
		parsed, err := time.Parse("2006-01-02", v)
		return v, err == nil && parsed.Format("2006-01-02") == v
	case "text", "sort":
		v, ok := value.(string)
		if !ok || len(v) < 1 || len(v) > 512 || !utf8.ValidString(v) || strings.IndexFunc(v, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			return nil, false
		}
		if p.kind == "sort" && !strings.EqualFold(v, "asc") && !strings.EqualFold(v, "desc") {
			return nil, false
		}
		return v, true
	case "identifier":
		if v, ok := value.(string); ok {
			return v, digits.MatchString(v)
		}
		if v, ok := value.(json.Number); ok {
			return v.String(), digits.MatchString(v.String())
		}
		if v, ok := value.(uint64); ok {
			return strconv.FormatUint(v, 10), true
		}
		if v, ok := value.(int64); ok {
			return strconv.FormatInt(v, 10), v >= 0
		}
		n, ok := number(value)
		if !ok || n < 0 || n > maxSafeInteger || math.Trunc(n) != n {
			return nil, false
		}
		return strconv.FormatFloat(n, 'f', 0, 64), true
	case "integer", "number", "favorite":
		n, ok := number(value)
		if !ok {
			return nil, false
		}
		if p.kind == "favorite" {
			return n, n == -1 || n == 0 || n == 1
		}
		if n < p.minimum || (p.maximum != 0 && n > p.maximum) {
			return nil, false
		}
		if p.kind == "integer" && (math.Trunc(n) != n || n > maxSafeInteger) {
			return nil, false
		}
		return n, true
	case "boolean":
		v, ok := value.(bool)
		return v, ok
	case "texts":
		values, ok := value.([]any)
		if !ok {
			if strings, yes := value.([]string); yes {
				values = make([]any, len(strings))
				for i, s := range strings {
					values[i] = s
				}
				ok = true
			}
		}
		if !ok || len(values) > 20 {
			return nil, false
		}
		output := make([]any, len(values))
		for i, v := range values {
			parsed, valid := normalizeParameter(optional("text"), v)
			if !valid {
				return nil, false
			}
			output[i] = parsed
		}
		return output, true
	}
	return nil, false
}

func (op Operation) ParameterNames() []string {
	names := make([]string, 0, len(op.parameters))
	for name := range op.parameters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (op Operation) Normalize(args map[string]any) (map[string]any, error) {
	for name := range args {
		if _, ok := op.parameters[name]; !ok {
			return nil, invalidArguments()
		}
	}
	values := make(map[string]any, len(op.parameters))
	for name, p := range op.parameters {
		value, found := args[name]
		if !found {
			if p.value != nil {
				value, found = p.value, true
			} else if p.required {
				return nil, invalidArguments()
			}
		}
		if !found {
			continue
		}
		parsed, valid := normalizeParameter(p, value)
		if !valid {
			return nil, invalidArguments()
		}
		values[name] = parsed
	}
	if op.rangeStart != "" {
		first, _ := time.Parse("2006-01-02", values[op.rangeStart].(string))
		last, _ := time.Parse("2006-01-02", values[op.rangeEnd].(string))
		days := int(last.Sub(first).Hours()/24) + 1
		if days < 1 || (op.MaxWindowDays != 0 && days > op.MaxWindowDays) {
			return nil, invalidArguments()
		}
	}
	if op.ID == "activities" {
		count := 0
		for _, name := range []string{"upperLatitude", "rightLongitude", "lowerLatitude", "leftLongitude"} {
			if _, ok := values[name]; ok {
				count++
			}
		}
		if count != 0 && count != 4 {
			return nil, invalidArguments()
		}
		if first, ok := values["startDate"].(string); ok {
			if last, exists := values["endDate"].(string); exists && first > last {
				return nil, invalidArguments()
			}
		}
		for _, pair := range [][2]string{{"minDistance", "maxDistance"}, {"minDuration", "maxDuration"}, {"minElevation", "maxElevation"}, {"lowerMaxDepth", "upperMaxDepth"}, {"minElapsedDuration", "maxElapsedDuration"}, {"minBottomTime", "maxBottomTime"}} {
			minimum, hasMinimum := values[pair[0]].(float64)
			maximum, hasMaximum := values[pair[1]].(float64)
			if hasMinimum && hasMaximum && minimum > maximum {
				return nil, invalidArguments()
			}
		}
	}
	return values, nil
}

func queryValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	}
	return ""
}

func (op Operation) Build(args map[string]any) (string, error) {
	values, err := op.Normalize(args)
	if err != nil {
		return "", err
	}
	pathNames := map[string]bool{}
	path := placeholder.ReplaceAllStringFunc(op.Path, func(match string) string {
		name := match[1 : len(match)-1]
		pathNames[name] = true
		return url.PathEscape(queryValue(values[name]))
	})
	query := url.Values{}
	for name, value := range values {
		if pathNames[name] {
			continue
		}
		if items, ok := value.([]any); ok {
			for _, item := range items {
				query.Add(name, queryValue(item))
			}
		} else {
			query.Add(name, queryValue(value))
		}
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return path, nil
}

func validKey(key string) bool {
	return len(key) > 0 && len(key) <= 8192 && utf8.ValidString(key) && strings.IndexFunc(key, func(r rune) bool { return unicode.IsSpace(r) || r < 32 || r == 127 }) < 0
}
