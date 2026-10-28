package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalKeyIsStoredAtomicallyAndRefusesSharedFilesAndLinks(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(directory, "key")
	for _, key := range []string{"synthetic-first-key", "synthetic-replacement-key"} {
		if err := SaveLocalKey(path, []byte(key)); err != nil {
			t.Fatal(err)
		}
		actual, err := ReadPrivateFile(path, 8192)
		if err != nil || !bytes.Equal(actual, []byte(key)) {
			t.Fatal("private key did not survive atomic replacement")
		}
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 1 {
		t.Fatal("temporary credential files remain")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if SaveLocalKey(path, []byte("must-not-overwrite")) == nil {
		t.Fatal("shared credential file accepted")
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if SaveLocalKey(link, []byte("must-not-overwrite")) == nil {
		t.Fatal("credential symlink accepted")
	}
	if SaveLocalKey("relative-key", []byte("must-not-write")) == nil {
		t.Fatal("relative credential path accepted")
	}
}
