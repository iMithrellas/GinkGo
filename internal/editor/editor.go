package editor

import (
	"bytes"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	TitlePrefix = "Title: "
	TagsPrefix  = "Tags: "
)

// ComposeContent builds the editor-ready content for a note.
//
// ComposeContent writes a header that includes the package's TitlePrefix followed by the given title and the TagsPrefix followed by the comma-separated tags, then a delimiter line `---` and finally the provided Markdown body. If body is non-empty, it is appended and ensured to end with a single trailing newline.
func ComposeContent(title string, tags []string, body string) string {
	var b bytes.Buffer
	b.WriteString("# GinkGo Note\n")
	b.WriteString("# Lines starting with '#' are ignored.\n")
	b.WriteString("# Set Title and Tags (comma-separated). After '---', write Markdown body.\n")
	b.WriteString(TitlePrefix)
	b.WriteString(title)
	b.WriteString("\n")
	b.WriteString(TagsPrefix)
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

// PreferredEditor returns the editor command to use by preferring the VISUAL environment
// variable, then EDITOR, and finally searching PATH for common editors ("nvim", "vim", "vi").
// It returns the chosen editor command, or an error if no editor can be determined.
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

// PathForID constructs a temporary file path for the given note id and optional namespace.
// If namespace is non-empty after trimming, it is URL path-escaped and prefixed to the filename as "<encoded>.<id>.ginkgo.md".
// If XDG_RUNTIME_DIR is set, the file is placed under XDG_RUNTIME_DIR/ginkgo/; otherwise it is placed under ~/.cache/ginkgo/edit/.
// Returns an error only if the user's home directory cannot be determined when falling back to the cache path.
func PathForID(id string, namespace string) (string, error) {
	prefix := ""
	if strings.TrimSpace(namespace) != "" {
		prefix = encodeNamespace(namespace) + "."
	}
	name := prefix + id + ".ginkgo.md"
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "ginkgo", name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "ginkgo", "edit", name), nil
}

// encodeNamespace trims whitespace from namespace and returns its URL path-escaped form; if the trimmed namespace is empty it returns an empty string.
func encodeNamespace(namespace string) string {
	trimmed := strings.TrimSpace(namespace)
	if trimmed == "" {
		return ""
	}
	return url.PathEscape(trimmed)
}

// ensureDirSecure ensures the directory that would contain the given path exists
// and has permission mode 0700. It returns any error encountered while creating
// the directory.
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
	// Honor VISUAL/EDITOR including flags by running via a shell wrapper.
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	var cmd *exec.Cmd
	if strings.TrimSpace(ed) != "" {
		cmd = exec.Command("sh", "-c", "$EDITORCMD \"$FILEPATH\"")
		cmd.Env = append(os.Environ(), "EDITORCMD="+ed, "FILEPATH="+path)
	} else {
		// Fallback to common terminal editors
		prog, err := PreferredEditor()
		if err != nil {
			return nil, false, err
		}
		cmd = exec.Command(prog, path)
	}
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

// ParseEditedNote extracts a note title, a list of tags, and the body from editor text.
// It reads header lines until a line containing only `---` marks the start of the body.
// Lines beginning with `#` are treated as comments and ignored while parsing the header.
// The title is taken from the header line prefixed by TitlePrefix; the tags are taken from
// the header line prefixed by TagsPrefix and parsed as comma-separated values (whitespace trimmed,
// empty tag entries ignored). The returned body contains the lines after the `---` delimiter with
// trailing newlines removed and surrounding whitespace trimmed.
func ParseEditedNote(s string) (title string, tags []string, body string) {
	lines := strings.Split(s, "\n")
	inBody := false
	var bodyLines []string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") && !inBody {
			continue
		}
		if !inBody {
			if strings.HasPrefix(line, strings.TrimSpace(TitlePrefix)) {
				title = strings.TrimSpace(strings.TrimPrefix(line, strings.TrimSpace(TitlePrefix)))

				continue
			}
			if strings.HasPrefix(line, strings.TrimSpace(TagsPrefix)) {
				raw := strings.TrimSpace(strings.TrimPrefix(line, strings.TrimSpace(TagsPrefix)))

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