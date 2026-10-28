package app

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/danchupin/garmin-mcp/internal/domain"
)

func ReadPrivateFile(path string, maximum int64) ([]byte, error) {
	invalid := func() ([]byte, error) {
		return nil, domain.NewError("PRIVATE_FILE", "Secret files must be private regular files with permissions 0600.", 500)
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 {
		return invalid()
	}
	file, err := os.Open(path)
	if err != nil {
		return invalid()
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0o077 != 0 {
		return invalid()
	}
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(value)) > maximum {
		return invalid()
	}
	return value, nil
}

func LocalKey(getenv func(string) string) (string, error) {
	value := getenv("GARMIN_PERSONAL_API_KEY")
	if path := getenv("GARMIN_PERSONAL_API_KEY_FILE"); path != "" {
		if value != "" {
			return "", domain.NewError("CONFIGURATION", "Choose one credential input method.", 500)
		}
		bytes, err := ReadPrivateFile(path, 8192)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(string(bytes))
		clear(bytes)
	}
	if value == "" {
		return "", domain.NewError("CONFIGURATION", "A private Garmin credential is required for local stdio.", 500)
	}
	return value, nil
}

func SafeMessage(err error) string {
	var known *domain.Error
	if errors.As(err, &known) {
		return known.Code + ": " + known.Message
	}
	return "INTERNAL_ERROR: The operation could not be completed."
}

func LocalToken(getenv func(string) string) (string, error) {
	if getenv("GARMIN_PERSONAL_API_KEY") != "" || getenv("GARMIN_PERSONAL_API_KEY_FILE") != "" {
		return "", domain.NewError("CONFIGURATION", "Choose either a Personal API key or a temporary access token, never both.", 500)
	}
	value := getenv("GARMIN_ACCESS_TOKEN")
	if path := getenv("GARMIN_ACCESS_TOKEN_FILE"); path != "" {
		if value != "" {
			return "", domain.NewError("CONFIGURATION", "Choose one temporary-token input method.", 500)
		}
		bytes, err := ReadPrivateFile(path, 8192)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(string(bytes))
		clear(bytes)
	}
	if value == "" {
		return "", domain.NewError("CONFIGURATION", "A temporary Garmin access token is required for stdio.", 500)
	}
	return value, nil
}
