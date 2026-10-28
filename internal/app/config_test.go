package app

import (
	"os"
	"path/filepath"
	"testing"
)

func environment(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLocalCredentialsNeverNeedAnHTTPProvider(t *testing.T) {
	if key, err := LocalKey(environment(map[string]string{"GARMIN_PERSONAL_API_KEY": "canary-local-key"})); err != nil || key != "canary-local-key" {
		t.Fatal("local secret input failed")
	}
	if _, err := LocalKey(environment(map[string]string{})); err == nil {
		t.Fatal("missing credential accepted")
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("canary-file-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := LocalKey(environment(map[string]string{"GARMIN_PERSONAL_API_KEY_FILE": path})); err != nil || key != "canary-file-key" {
		t.Fatal("private credential input failed")
	}
	if _, err := LocalKey(environment(map[string]string{"GARMIN_PERSONAL_API_KEY": "canary", "GARMIN_PERSONAL_API_KEY_FILE": path})); err == nil {
		t.Fatal("ambiguous credentials accepted")
	}
}

func TestPrivateFileRejectsSymlinkPermissionsAndOversize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("synthetic-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(path, 2); err == nil {
		t.Fatal("oversized secret accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(path, 8192); err == nil {
		t.Fatal("public secret accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(link, 8192); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestTemporaryTokenInputRejectsAmbiguity(t *testing.T) {
	for _, env := range []map[string]string{
		{"GARMIN_PERSONAL_API_KEY": "synthetic-key", "GARMIN_ACCESS_TOKEN": "synthetic-token"},
		{"GARMIN_ACCESS_TOKEN": "synthetic-token", "GARMIN_ACCESS_TOKEN_FILE": "/unread/key"},
		{},
	} {
		if _, err := LocalToken(environment(env)); err == nil {
			t.Fatal("ambiguous or missing secret accepted")
		}
	}
	if got, err := LocalToken(environment(map[string]string{"GARMIN_ACCESS_TOKEN": "synthetic-token"})); err != nil || got != "synthetic-token" {
		t.Fatal("temporary token rejected")
	}
}
