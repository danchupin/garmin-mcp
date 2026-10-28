package app

import (
	"os"
	"path/filepath"

	"github.com/danchupin/garmin-personal-mcp/internal/domain"
)

func LocalKeyPath(getenv func(string) string) (string, error) {
	if path := getenv("GARMIN_PERSONAL_API_KEY_FILE"); path != "" {
		if !filepath.IsAbs(path) {
			return "", domain.NewError("CONFIGURATION", "The local credential path must be absolute.", 500)
		}
		return path, nil
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", domain.NewError("CONFIGURATION", "The local configuration directory is unavailable.", 500)
	}
	return filepath.Join(directory, "GarminMCP", "personal-api-key"), nil
}

func SaveLocalKey(path string, key []byte) error {
	invalid := func() error {
		return domain.NewError("PRIVATE_FILE", "The local key needs a private directory (0700) and a regular file (0600).", 500)
	}
	if !filepath.IsAbs(path) || len(key) == 0 || len(key) > 8192 {
		return invalid()
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return invalid()
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return invalid()
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return invalid()
		}
	} else if !os.IsNotExist(err) {
		return invalid()
	}
	file, err := os.CreateTemp(directory, ".personal-api-key-")
	if err != nil {
		return invalid()
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(key); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil || os.Rename(file.Name(), path) != nil {
		return invalid()
	}
	return nil
}
