package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danchupin/garmin-mcp/internal/domain"
	"github.com/danchupin/garmin-mcp/internal/personal"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type serviceCall struct {
	Principal  domain.Principal
	Operation  string
	Parameters map[string]any
	Export     bool
}
type fakeService struct {
	mu       sync.Mutex
	calls    []serviceCall
	reads    []domain.Principal
	response domain.Result
	failure  error
	perUser  bool
	artifact *domain.Artifact
}

func (s *fakeService) ConnectionStatus(p domain.Principal) (*domain.Connection, error) {
	return &domain.Connection{ID: p.Subject, Region: "global", Version: 1}, nil
}
func (s *fakeService) Execute(_ context.Context, p domain.Principal, operation string, args map[string]any, export bool) (domain.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, serviceCall{p, operation, args, export})
	if s.failure != nil {
		return domain.Result{}, s.failure
	}
	if s.perUser {
		return domain.Result{Kind: "json", Data: map[string]any{"account": p.Subject}}, nil
	}
	return s.response, nil
}
func (s *fakeService) ReadFile(_ context.Context, p domain.Principal, uri string) (domain.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, p)
	if s.artifact == nil || uri != s.artifact.URI || p.Subject != "alice" || !p.HasScope("activities:export") {
		return domain.File{}, domain.NewError("FORBIDDEN", "synthetic-secret-error", 403)
	}
	if s.failure != nil {
		return domain.File{}, s.failure
	}
	return domain.File{Artifact: *s.artifact, Bytes: []byte("synthetic-fit-bytes")}, nil
}
func (s *fakeService) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.calls) }

func sdkClient(t *testing.T, service domain.Service, principal domain.Principal, version string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := New(service, principal)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "garmin-integration-test", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	cs, err := client.Connect(ctx, ct, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		_ = ss.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	return cs
}
func call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func structured(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool error: %v", result.Content)
	}
	value, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("missing structured result: %#v", result.StructuredContent)
	}
	return value
}
func textOf(result *mcp.CallToolResult) string {
	var text string
	for _, item := range result.Content {
		if value, ok := item.(*mcp.TextContent); ok {
			text += value.Text
		}
	}
	return text
}
func principal(subject string, scopes ...string) domain.Principal {
	return domain.Principal{Issuer: "https://mcp.test", Subject: subject, Scopes: scopes}
}

func TestOfficialSDKCatalogueAndCapabilitySchemas(t *testing.T) {
	service := &fakeService{}
	cs := sdkClient(t, service, principal("alice", "wellness:read"), ProtocolVersion)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatal("missing read-only hints")
		}
	}
	if !reflect.DeepEqual(names, []string{"connection_status", "list_garmin_capabilities", "read_garmin"}) {
		t.Fatal(names)
	}
	encoded, _ := json.Marshal(tools)
	t.Logf("tool catalogue: %d bytes", len(encoded))
	if len(encoded) > 7*1024 {
		t.Fatalf("oversized tool catalogue: %d bytes", len(encoded))
	}
	all := structured(t, call(t, cs, "list_garmin_capabilities", map[string]any{}))
	entries := all["operations"].([]any)
	expected := 0
	for _, op := range personal.Operations() {
		if op.Scope == "wellness:read" {
			expected++
		}
	}
	if len(entries) != expected {
		t.Fatal(len(entries), expected)
	}
	for _, entry := range entries {
		item := entry.(map[string]any)
		if item["scope"] != "wellness:read" || item["input_schema"] != nil {
			t.Fatal(item)
		}
	}
	specific := structured(t, call(t, cs, "list_garmin_capabilities", map[string]any{"operation": "sleep_daily"}))["operations"].([]any)
	if len(specific) != 1 || specific[0].(map[string]any)["input_schema"] == nil {
		t.Fatal(specific)
	}
	if !call(t, cs, "list_garmin_capabilities", map[string]any{"operation": "profile"}).IsError {
		t.Fatal("ungranted schema disclosed")
	}
}

func TestPublishedOutputSchemasValidateRealResultsAndRejectEnvelopeDrift(t *testing.T) {
	artifact := &domain.Artifact{ID: "f42", URI: "garmin://files/f42", Name: "activity-42.bin", MIMEType: "application/octet-stream", Size: 19, ExpiresAt: time.Now().Add(time.Minute)}
	service := &fakeService{response: domain.Result{Kind: "json", Data: []any{map[string]any{"score": 84}, map[string]any{"score": 90}}}, artifact: artifact}
	cs := sdkClient(t, service, principal("alice", personal.Scopes()...), ProtocolVersion)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Resolved{}
	for _, tool := range tools.Tools {
		if tool.OutputSchema == nil {
			t.Fatalf("missing output schema: %s", tool.Name)
		}
		encoded, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(encoded, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		schemas[tool.Name] = resolved
	}
	validate := func(name string, result *mcp.CallToolResult) {
		t.Helper()
		data := structured(t, result)
		if err := schemas[name].Validate(data); err != nil {
			t.Fatalf("%s real output mismatches schema: %v", name, err)
		}
		data["unexpected_outer_field"] = true
		if err := schemas[name].Validate(data); err == nil {
			t.Fatalf("%s output envelope permits unknown fields", name)
		}
		delete(data, "unexpected_outer_field")
	}
	validate("list_garmin_capabilities", call(t, cs, "list_garmin_capabilities", map[string]any{}))
	validate("list_garmin_capabilities", call(t, cs, "list_garmin_capabilities", map[string]any{"operation": "activities"}))
	validate("connection_status", call(t, cs, "connection_status", map[string]any{}))
	validate("read_garmin", call(t, cs, "read_garmin", map[string]any{"operation": "daily_summary", "fields": []string{"score"}, "max_items": 1}))
	service.mu.Lock()
	service.response = domain.Result{Kind: "file", Artifact: artifact}
	service.mu.Unlock()
	validate("read_garmin", call(t, cs, "read_garmin", map[string]any{"operation": "activity_file", "parameters": map[string]any{"activityId": "42"}}))
	invalid := map[string]any{"source": "garmin_personal_api", "operation": "daily_summary", "data": nil}
	if err := schemas["read_garmin"].Validate(invalid); err == nil {
		t.Fatal("preview without projection accepted")
	}
	invalid["artifact"] = map[string]any{"id": "bad"}
	if err := schemas["read_garmin"].Validate(invalid); err == nil {
		t.Fatal("invalid artifact accepted")
	}
	artifactJSON, _ := json.Marshal(artifact)
	var artifactValue any
	_ = json.Unmarshal(artifactJSON, &artifactValue)
	invalid["artifact"] = artifactValue
	if err := schemas["read_garmin"].Validate(invalid); err == nil {
		t.Fatal("artifact with preview data accepted")
	}
}

func TestOfficialSDKDistinctUsersAndImmutablePrincipal(t *testing.T) {
	service := &fakeService{perUser: true}
	alice := principal("alice", "wellness:read")
	a := sdkClient(t, service, alice, ProtocolVersion)
	alice.Scopes[0] = "nutrition:read"
	b := sdkClient(t, service, principal("bob", "wellness:read"), ProtocolVersion)
	for _, item := range []struct {
		session *mcp.ClientSession
		owner   string
	}{{a, "alice"}, {b, "bob"}} {
		result := call(t, item.session, "read_garmin", map[string]any{"operation": "daily_summary", "parameters": map[string]any{}})
		data := structured(t, result)["data"].(map[string]any)
		if data["account"] != item.owner {
			t.Fatal(data)
		}
		if strings.Contains(textOf(result), `"account"`) {
			t.Fatal("modern response duplicates JSON in text")
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.calls) != 2 || service.calls[0].Principal.Subject != "alice" || service.calls[1].Principal.Subject != "bob" || !service.calls[0].Principal.HasScope("wellness:read") {
		t.Fatal(service.calls)
	}
}

func TestToolArgumentsCannotInjectIdentityCredentialsOrBypassScopes(t *testing.T) {
	service := &fakeService{perUser: true}
	cs := sdkClient(t, service, principal("alice", "wellness:read"), ProtocolVersion)
	for _, args := range []map[string]any{
		{"operation": "daily_summary", "user_id": "bob"},
		{"operation": "daily_summary", "api_key": "synthetic-secret-key"},
		{"operation": "daily_summary", "authorization": "Bearer synthetic"},
		{"operation": "daily_summary", "parameters": map[string]any{"user_id": "bob"}},
		{"operation": "profile"},
		{"operation": "activity_file", "parameters": map[string]any{"activityId": "1"}},
		{"operation": "daily_summary", "max_items": 0},
		{"operation": "daily_summary", "max_items": 51},
		{"operation": "daily_summary", "offset": -1},
		{"operation": "daily_summary", "fields": []string{}},
		{"operation": "daily_summary", "fields": []string{"foo[0]"}},
		{"operation": "daily_summary", "export_json": true, "fields": []string{"account"}},
		{"operation": "daily_summary", "parameters": nil},
		{"operation": "daily_summary", "fields": nil},
		{"operation": "daily_summary", "max_items": nil},
		{"operation": "daily_summary", "offset": nil},
		{"operation": "daily_summary", "export_json": nil},
	} {
		result := call(t, cs, "read_garmin", args)
		if !result.IsError {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
	if service.count() != 0 {
		t.Fatal("invalid input reached service")
	}
	if !call(t, cs, "connection_status", map[string]any{"user_id": "bob"}).IsError {
		t.Fatal("status identity injection accepted")
	}
	for _, args := range []map[string]any{{"group": ""}, {"operation": ""}, {"group": nil}, {"operation": "sleep_daily", "user_id": "bob"}} {
		if !call(t, cs, "list_garmin_capabilities", args).IsError {
			t.Fatal("capabilities accepted invalid arguments", args)
		}
	}
}

func TestLegacyFallbackHasFullJSONAndModernHasShortText(t *testing.T) {
	for _, version := range []string{ProtocolVersion, "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			service := &fakeService{response: domain.Result{Kind: "json", Data: map[string]any{"sleep_score": 84}}}
			cs := sdkClient(t, service, principal("alice", "wellness:read"), version)
			result := call(t, cs, "read_garmin", map[string]any{"operation": "daily_summary"})
			data := structured(t, result)
			text := textOf(result)
			var parsed map[string]any
			err := json.Unmarshal([]byte(text), &parsed)
			if version == ProtocolVersion {
				if err == nil {
					t.Fatal("modern text duplicated JSON")
				}
			} else {
				if err != nil || !reflect.DeepEqual(parsed, data) {
					t.Fatal("legacy fallback omitted structured data", text, err)
				}
			}
		})
	}
}

func TestProjectionRootArrayColumnsStayAligned(t *testing.T) {
	data := []any{map[string]any{"calendarDate": "a", "score": 80}, map[string]any{"calendarDate": "b"}, map[string]any{"calendarDate": "c", "score": 90}}
	value, metadata, err := project(data, []string{"calendarDate", "score"}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	columns := value.(map[string]any)
	if !reflect.DeepEqual(columns["calendarDate"], []any{"b", "c"}) || !reflect.DeepEqual(columns["score"], []any{nil, 90}) {
		t.Fatal(columns)
	}
	arrays := metadata["arrays"].([]any)
	if len(arrays) != 1 {
		t.Fatal("root array paginated more than once", arrays)
	}
	page := arrays[0].(map[string]any)
	if page["offset"] != 1 || page["returned"] != 2 || page["total"] != 3 {
		t.Fatal(page)
	}
}

func TestProjectionNestedArraysApplyOffsetOnlyAtFirstArray(t *testing.T) {
	data := map[string]any{"hrv": []any{map[string]any{"readings": []any{1, 2, 3}}, map[string]any{"readings": []any{4, 5, 6}}, map[string]any{"readings": []any{7, 8, 9}}}}
	value, metadata, err := project(data, []string{"hrv.readings"}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value.(map[string]any)["hrv.readings"], []any{[]any{4}}) {
		t.Fatal(value)
	}
	arrays := metadata["arrays"].([]any)
	if len(arrays) != 2 || arrays[1].(map[string]any)["offset"] != 0 {
		t.Fatal(arrays)
	}
	if _, _, err := project(data, []string{"hrv.missing"}, 1, 0); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestProjectionMissingCellsOutsidePageAndEmptyArrays(t *testing.T) {
	data := []any{map[string]any{}, map[string]any{"score": 90}}
	value, _, err := project(data, []string{"score"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value.(map[string]any)["score"], []any{nil}) {
		t.Fatal(value)
	}
	value, _, err = project(map[string]any{"items": []any{}}, []string{"items.score"}, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value.(map[string]any)["items.score"], []any{}) {
		t.Fatal(value)
	}
}

func TestProjectionPreservesScalarTuplesAndReportsBounds(t *testing.T) {
	data := map[string]any{"readings": []any{[]any{1000, 50}, []any{2000, 55}, []any{3000, 60}}}
	value, metadata, err := project(data, nil, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value.(map[string]any)["readings"], []any{[]any{2000, 55}}) {
		t.Fatal(value)
	}
	if len(metadata["omitted_paths"].([]string)) == 0 {
		t.Fatal("silent array truncation")
	}
	wide := map[string]any{}
	for i := 0; i < 30; i++ {
		wide[fmt.Sprintf("field%02d", i)] = i
	}
	value, metadata, err = project(wide, nil, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.(map[string]any)) != 24 || len(metadata["omitted_paths"].([]string)) != 6 {
		t.Fatal(value, metadata)
	}
	deep := map[string]any{}
	cursor := deep
	for i := 0; i < 11; i++ {
		next := map[string]any{}
		cursor["child"] = next
		cursor = next
	}
	cursor["value"] = 42
	_, metadata, err = project(deep, nil, 10, 0)
	if err != nil || len(metadata["omitted_paths"].([]string)) == 0 {
		t.Fatal("silent depth truncation", metadata, err)
	}
}

func TestProjectionPathDisplayCollisionsCannotReuseDifferentArrays(t *testing.T) {
	data := map[string]any{"a": map[string]any{"b": []any{1, 2}}, "a.b": []any{9, 8}}
	value, _, err := project(data, nil, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	output := value.(map[string]any)
	if !reflect.DeepEqual(output["a"].(map[string]any)["b"], []any{1}) || !reflect.DeepEqual(output["a.b"], []any{9}) {
		t.Fatal(output)
	}
}

func TestOutputBudgetAndUnexpectedErrorsDoNotLeakSecrets(t *testing.T) {
	for _, failure := range []error{nil, errors.New("synthetic-secret-canary")} {
		service := &fakeService{failure: failure, response: domain.Result{Kind: "json", Data: strings.Repeat("x", 17000)}}
		cs := sdkClient(t, service, principal("alice", "wellness:read"), ProtocolVersion)
		result := call(t, cs, "read_garmin", map[string]any{"operation": "daily_summary"})
		if !result.IsError || strings.Contains(textOf(result), "synthetic-secret-canary") {
			t.Fatal(result)
		}
		if failure == nil && !strings.Contains(textOf(result), "RESULT_TOO_LARGE") {
			t.Fatal(textOf(result))
		}
	}
}

func TestFileExportIsLinkedAndReadOnlyByOwner(t *testing.T) {
	artifact := &domain.Artifact{ID: "f42", URI: "garmin://files/f42", Name: "activity-42.bin", MIMEType: "application/octet-stream", Size: 19, ExpiresAt: time.Now().Add(time.Minute)}
	service := &fakeService{artifact: artifact, response: domain.Result{Kind: "file", Artifact: artifact}}
	alice := sdkClient(t, service, principal("alice", "activities:export"), ProtocolVersion)
	result := call(t, alice, "read_garmin", map[string]any{"operation": "activity_file", "parameters": map[string]any{"activityId": "42"}})
	data := structured(t, result)
	if data["artifact"] == nil || strings.Contains(textOf(result), "synthetic-fit-bytes") {
		t.Fatal(result)
	}
	links := 0
	for _, item := range result.Content {
		if link, ok := item.(*mcp.ResourceLink); ok && link.URI == artifact.URI {
			links++
		}
	}
	if links != 1 {
		t.Fatal("missing resource link")
	}
	service.mu.Lock()
	if len(service.reads) != 0 {
		t.Fatal("file read without explicit request")
	}
	service.mu.Unlock()
	file, err := alice.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: artifact.URI})
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Contents) != 1 || string(file.Contents[0].Blob) != "synthetic-fit-bytes" {
		t.Fatal(file)
	}
	bob := sdkClient(t, service, principal("bob", "activities:export"), ProtocolVersion)
	if _, err := bob.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: artifact.URI}); err == nil || strings.Contains(err.Error(), "synthetic-secret-error") {
		t.Fatal("foreign resource read accepted or unsafe error", err)
	}
}

func TestRevokedGarminTokenReturnsSafeLocalError(t *testing.T) {
	service := &fakeService{failure: domain.NewError("UNAUTHORIZED", "Garmin authorization failed. Check the local key.", 401)}
	client := sdkClient(t, service, principal("alice", "profile:read"), ProtocolVersion)
	result := call(t, client, "read_garmin", map[string]any{"operation": "profile"})
	if !result.IsError || !strings.Contains(textOf(result), "UNAUTHORIZED") || result.Meta["mcp/www_authenticate"] != nil {
		t.Fatal("local authorization error was hidden or included HTTP authorization metadata")
	}
}

func TestResourceErrorsDistinguishUpstreamFailuresWithoutLeakingDetails(t *testing.T) {
	artifact := &domain.Artifact{URI: "garmin://files/f42"}
	for _, code := range []string{"GARMIN_UPSTREAM", "RESPONSE_TOO_LARGE", "UNAUTHORIZED", "GARMIN_RATE_LIMITED"} {
		service := &fakeService{artifact: artifact, failure: domain.NewError(code, "synthetic-secret-error", 502)}
		client := sdkClient(t, service, principal("alice", "activities:export"), ProtocolVersion)
		_, err := client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: artifact.URI})
		if err == nil || !strings.Contains(err.Error(), code) || strings.Contains(err.Error(), "synthetic-secret-error") {
			t.Fatal("upstream resource failure was masked or exposed raw diagnostics")
		}
	}
}
