package diffparse

import (
	"errors"
	"strconv"
	"strings"
)

// File is one file's changes in a unified diff.
type File struct {
	Path  string
	Added []int // head line numbers of the added lines
}

var errMalformed = errors.New("malformed diff")

// Parse reads a unified git diff, as the git provider's API returns it, into
// each file's added lines. Review comments are anchored to these lines.
func Parse(diff string) ([]File, error) {
	var files []File
	var cur *File
	line := 0
	for _, text := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(text, "diff --git "):
			files = append(files, File{})
			cur = &files[len(files)-1]
		case cur == nil:
			return nil, errMalformed
		case strings.HasPrefix(text, "+++ "):
			cur.Path = strings.TrimPrefix(strings.TrimPrefix(text, "+++ "), "b/")
		case strings.HasPrefix(text, "--- "), strings.HasPrefix(text, "index "):
		case strings.HasPrefix(text, "@@ "):
			start, err := hunkStart(text)
			if err != nil {
				return nil, err
			}
			line = start
		case strings.HasPrefix(text, "+"):
			cur.Added = append(cur.Added, line)
			line++
		case strings.HasPrefix(text, "-"), text == `\ No newline at end of file`:
		default:
			line++
		}
	}
	return files, nil
}

// hunkStart returns the first head line of a hunk header such as
// "@@ -10,4 +12,6 @@ func name".
func hunkStart(header string) (int, error) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
		return 0, errMalformed
	}
	start, _, _ := strings.Cut(fields[2][1:], ",")
	return strconv.Atoi(start)
}
