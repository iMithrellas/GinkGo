package editor

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ComposeContent creates the text presented to the editor.
func ComposeContent(title string, tags []string, body string) string {
	var b bytes.Buffer
	b.WriteString("# GinkGo Note\n")
	b.WriteString("# Lines starting with '#' are ignored.\n")
	b.WriteString("# Set Title and Tags (comma-separated). After '---', write Markdown body.\n")
	b.WriteString("Title: ")
	b.WriteString(title)
	b.WriteString("\n")
	b.WriteString("Tags: ")
	if len(tags) > 0 {
		b.WriteString(strings.Join(tags, ", "))
	}
	b.WriteString("\n---\n")
	if body != "" {
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		b.WriteString(body)
	}
	return b.String()
}

// PreferredEditor finds a suitable editor from env or common defaults.
func PreferredEditor() (string, error) {
	if v := os.Getenv("VISUAL"); v != "" {
		return v, nil
	}
	if e := os.Getenv("EDITOR"); e != "" {
		return e, nil
	}
	for _, cand := range []string{"nvim", "vim", "vi"} {
		if p, err := exec.LookPath(cand); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no editor found; set $EDITOR or $VISUAL")
}

// PathForID returns a temp file path for a note ID.
func PathForID(id string) (string, error) {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "ginkgo", id+".md"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "ginkgo", "edit", id+".md"), nil
}

func ensureDirSecure(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return nil
}

func writeFile0600(path string, data []byte) error {
	if err := ensureDirSecure(path); err != nil {
		return err
	}
	return os.WriteFile(path, data, fs.FileMode(0o600))
}

// OpenAt opens the editor at path with initial content and returns final bytes and whether it changed.
func OpenAt(path string, initial []byte) (final []byte, changed bool, err error) {
	if err := writeFile0600(path, initial); err != nil {
		return nil, false, err
	}
	ed, err := PreferredEditor()
	if err != nil {
		return nil, false, err
	}
	cmd := exec.Command(ed, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, false, err
	}
	out, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return out, !bytes.Equal(out, initial), nil
}

// PrepareAt writes the initial content to the given path with secure perms.
func PrepareAt(path string, initial []byte) error {
	return writeFile0600(path, initial)
}

// ParseEditedNote extracts title, tags and body from the editor output.
func ParseEditedNote(s string) (title string, tags []string, body string) {
	lines := strings.Split(s, "\n")
	inBody := false
	var bodyLines []string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") && !inBody {
			continue
		}
		if !inBody {
			if strings.HasPrefix(line, "Title:") {
				title = strings.TrimSpace(strings.TrimPrefix(line, "Title:"))
				continue
			}
			if strings.HasPrefix(line, "Tags:") {
				raw := strings.TrimSpace(strings.TrimPrefix(line, "Tags:"))
				if raw != "" {
					for _, t := range strings.Split(raw, ",") {
						tt := strings.TrimSpace(t)
						if tt != "" {
							tags = append(tags, tt)
						}
					}
				}
				continue
			}
			if strings.TrimSpace(line) == "---" {
				inBody = true
				continue
			}
			// ignore other header lines
			continue
		}
		bodyLines = append(bodyLines, line)
	}
	body = strings.TrimRight(strings.Join(bodyLines, "\n"), "\n")
	return title, tags, strings.TrimSpace(body)
}

// FirstLine returns the first trimmed line, squashed and truncated.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
