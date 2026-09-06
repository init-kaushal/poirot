package prompts

import (
	"bufio"
	"crypto/sha256"
	"embed"
	"fmt"
	"regexp"
	"strings"
)

//go:embed *.txt
var files embed.FS

var headerRe = regexp.MustCompile(`^# (v\d+)$`)

func Load(name string) (text string, version string, err error) {
	raw, err := files.ReadFile(name + ".txt")
	if err != nil {
		return "", "", fmt.Errorf("prompts: %w", err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	if !sc.Scan() {
		return "", "", fmt.Errorf("prompts: %s is empty", name)
	}
	m := headerRe.FindStringSubmatch(strings.TrimSpace(sc.Text()))
	if m == nil {
		return "", "", fmt.Errorf("prompts: %s first line must be '# vN'", name)
	}
	sum := sha256.Sum256(raw)
	body := strings.TrimPrefix(string(raw), sc.Text())
	body = strings.TrimPrefix(body, "\n")
	return body, fmt.Sprintf("%s+%x", m[1], sum[:4]), nil
}
