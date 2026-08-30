package source

import (
	"fmt"
	"os"
	"strings"
)

// fileSource is a file whose entire contents are the version: the VERSION-file
// convention.
type fileSource struct{ base }

func (s *fileSource) Read() (string, error) {
	b, err := os.ReadFile(s.abs())
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", s.rel)
	}
	if strings.ContainsAny(v, "\r\n") {
		return "", fmt.Errorf("%s holds more than one line; a version file must contain only the version", s.rel)
	}
	return v, nil
}

// Write replaces the file's contents, keeping whatever trailing newline
// convention it already had so the diff is one line either way.
func (s *fileSource) Write(version string) error {
	trailing := ""
	if old, err := os.ReadFile(s.abs()); err == nil && strings.HasSuffix(string(old), "\n") {
		trailing = "\n"
	}
	return writeKeepingMode(s.abs(), []byte(version+trailing))
}
