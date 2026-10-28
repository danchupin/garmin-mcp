package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danchupin/garmin-personal-mcp/internal/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const helperMarker = "_GARMIN_STDIO_TEST_HELPER"
const stdioCanary = "synthetic-stdio-key-never-real-802871"

type rejectNetwork struct{ calls atomic.Int32 }

func (r *rejectNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return nil, errors.New("network calls are forbidden in the stdio smoke test")
}

type tokenCommandTransport struct{ calls atomic.Int32 }

func (transport *tokenCommandTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	if request.URL.Host != "personal.connectapi.garmin.com" || request.URL.Path != "/personalapitoken-service/personal-api/personal-api-key/token" || request.Method != "POST" || request.Header.Get("Authorization") != "ApiKey synthetic-local-command-key" {
		return nil, errors.New("unexpected local token exchange")
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(os.Getenv("_GARMIN_TOKEN_FIXTURE")))}, nil
}

func TestStdioHelperProcess(t *testing.T) {
	if os.Getenv(helperMarker) != "1" {
		return
	}
	transport := &rejectNetwork{}
	keyTransport := &tokenCommandTransport{}
	keyMode := os.Getenv("_GARMIN_STDIO_KEYMODE") == "1"
	if keyMode {
		http.DefaultTransport = keyTransport
	} else {
		http.DefaultTransport = transport
	}
	err := run(context.Background(), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, app.SafeMessage(err))
		os.Exit(1)
	}
	if transport.calls.Load() != 0 || (keyMode && keyTransport.calls.Load() != 1) {
		fmt.Fprintln(os.Stderr, "Unexpected network request.")
		os.Exit(2)
	}
	os.Exit(0)
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }

func TestCLIRejectsRetiredCommandsAndCredentialArguments(t *testing.T) {
	for _, arguments := range [][]string{{"serve"}, {"token"}, {"init"}, {"purge-legacy"}, {"login", "synthetic-secret"}, {"stdio", "extra"}} {
		if err := run(context.Background(), arguments); err == nil || !strings.HasPrefix(app.SafeMessage(err), "CONFIGURATION:") {
			t.Fatal("unsupported command or credential argument accepted")
		}
	}
}

func TestOfficialSDKStdioSubprocessUsesSyntheticCredentialWithoutNetwork(t *testing.T) {
	for _, mode := range []string{"temporary-token", "local-key"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStdioHelperProcess$")
			for _, entry := range os.Environ() {
				name, _, _ := strings.Cut(entry, "=")
				if !strings.HasPrefix(name, "GARMIN_") && name != helperMarker {
					command.Env = append(command.Env, entry)
				}
			}
			payload, _ := json.Marshal(map[string]any{"sub": stdioCanary, "exp": time.Now().Add(24 * time.Hour).Unix()})
			temporaryToken := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic-signature"
			command.Env = append(command.Env, helperMarker+"=1", "GARMIN_REGION=global", "GARMIN_SCOPES=wellness:read activities:read")
			if mode == "local-key" {
				keyPath := filepath.Join(t.TempDir(), "key")
				if err := os.WriteFile(keyPath, []byte("synthetic-local-command-key"), 0600); err != nil {
					t.Fatal(err)
				}
				command.Env = append(command.Env, "GARMIN_PERSONAL_API_KEY_FILE="+keyPath, "_GARMIN_STDIO_KEYMODE=1", "_GARMIN_TOKEN_FIXTURE="+temporaryToken)
			} else {
				command.Env = append(command.Env, "GARMIN_ACCESS_TOKEN="+temporaryToken)
			}
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr lockedBuffer
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if command.ProcessState == nil {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			var output lockedBuffer
			transport := &mcp.IOTransport{Reader: struct {
				io.Reader
				io.Closer
			}{Reader: io.TeeReader(stdout, &output), Closer: stdout}, Writer: stdin, MaxLineLength: 65_536}
			client := mcp.NewClient(&mcp.Implementation{Name: "stdio-subprocess-test", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
			session, err := client.Connect(ctx, transport, nil)
			if err != nil {
				t.Fatalf("stdio connect: %v; stderr: %s", err, stderr.String())
			}
			defer session.Close()
			tools, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(tools.Tools) != 3 {
				t.Fatalf("expected three tools, got %d", len(tools.Tools))
			}
			for _, tool := range tools.Tools {
				if tool.OutputSchema == nil {
					t.Fatalf("tool %s has no output schema", tool.Name)
				}
			}
			call := func(name string, args map[string]any) *mcp.CallToolResult {
				t.Helper()
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
				if err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(result)
				if bytes.Contains(encoded, []byte(stdioCanary)) || bytes.Contains(encoded, []byte(temporaryToken)) {
					t.Fatal("credential exposed by a tool result")
				}
				return result
			}
			capabilities := call("list_garmin_capabilities", map[string]any{})
			if capabilities.IsError {
				t.Fatal("capability discovery failed")
			}
			data, ok := capabilities.StructuredContent.(map[string]any)
			if !ok {
				t.Fatal("missing capability data")
			}
			operations, ok := data["operations"].([]any)
			if !ok || len(operations) == 0 {
				t.Fatal("no operations advertised")
			}
			for _, entry := range operations {
				scope := entry.(map[string]any)["scope"]
				if scope != "wellness:read" && scope != "activities:read" {
					t.Fatalf("unexpected scope %v", scope)
				}
			}
			status := call("connection_status", map[string]any{})
			if status.IsError || status.StructuredContent.(map[string]any)["connected"] != true {
				t.Fatal("local credential connection missing")
			}
			forbidden := call("read_garmin", map[string]any{"operation": "profile"})
			if !forbidden.IsError {
				t.Fatal("ungranted operation accepted")
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); err != nil {
				t.Fatalf("stdio helper: %v; stderr: %s", err, stderr.String())
			}
			if strings.Contains(output.String(), stdioCanary) || strings.Contains(stderr.String(), stdioCanary) || strings.Contains(output.String(), temporaryToken) || strings.Contains(stderr.String(), temporaryToken) {
				t.Fatal("credential exposed in stdio output")
			}
			for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				if !json.Valid([]byte(line)) {
					t.Fatalf("non-JSON text in stdout: %q", line)
				}
			}
		})
	}
}
