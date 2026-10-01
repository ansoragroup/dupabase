package platform

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var dumpCopy = regexp.MustCompile(`(?i)^\s*COPY\s+.+\s+FROM\s+stdin\s*;\s*$`)
var dollarTag = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$`)

// dumpSQLState distinguishes statement syntax from quoted data/function bodies.
// This filter is a compatibility tool, never a privilege/security boundary.
type dumpSQLState struct {
	quote       byte
	dollar      string
	block       int
	inStatement bool
}

func (s *dumpSQLState) consume(line string) {
	for i := 0; i < len(line); i++ {
		if s.dollar != "" {
			if strings.HasPrefix(line[i:], s.dollar) {
				i += len(s.dollar) - 1
				s.dollar = ""
			}
			continue
		}
		if s.quote != 0 {
			if line[i] == '\\' && s.quote == '\'' {
				i++
				continue
			}
			if line[i] == s.quote {
				if i+1 < len(line) && line[i+1] == s.quote {
					i++
				} else {
					s.quote = 0
				}
			}
			continue
		}
		if s.block > 0 {
			if strings.HasPrefix(line[i:], "/*") {
				s.block++
				i++
			} else if strings.HasPrefix(line[i:], "*/") {
				s.block--
				i++
			}
			continue
		}
		if strings.HasPrefix(line[i:], "--") {
			break
		}
		if strings.HasPrefix(line[i:], "/*") {
			s.block++
			i++
			continue
		}
		if line[i] == '\'' || line[i] == '"' {
			s.quote = line[i]
			s.inStatement = true
			continue
		}
		if line[i] == '$' {
			if tag := dollarTag.FindString(line[i:]); tag != "" {
				s.dollar = tag
				i += len(tag) - 1
				s.inStatement = true
				continue
			}
		}
		if line[i] == ';' {
			s.inStatement = false
		} else if line[i] != ' ' && line[i] != '\t' && line[i] != '\r' {
			s.inStatement = true
		}
	}
}

func filterSQLFile(inputPath string) (string, error) { return filterSQLFileOptions(inputPath, true) }

func filterSQLFileOptions(inputPath string, skipAuth bool) (result string, err error) {
	input, err := os.Open(inputPath)
	if err != nil {
		return "", fmt.Errorf("open input: %w", err)
	}
	defer input.Close()
	output, err := os.CreateTemp("", "import-filtered-*.sql")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			output.Close()
			os.Remove(output.Name())
		}
	}()
	writer := bufio.NewWriter(output)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var state dumpSQLState
	copying, skipCopy, skipStatement := false, false, false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if copying {
			if !skipCopy {
				if _, err = fmt.Fprintln(writer, line); err != nil {
					return "", err
				}
			}
			if line == "\\." {
				copying, skipCopy = false, false
			}
			continue
		}
		atStart := !state.inStatement && state.quote == 0 && state.dollar == "" && state.block == 0
		if atStart {
			// Replace dump-owned restriction markers with the worker's fresh key.
			// A dump must never change the selected target database.
			if strings.HasPrefix(trimmed, "\\restrict ") || strings.HasPrefix(trimmed, "\\unrestrict ") || strings.HasPrefix(trimmed, "\\connect ") {
				continue
			}
			if skipAuth {
				for _, pattern := range sqlFilterPatterns {
					if pattern.MatchString(line) {
						skipStatement = true
						break
					}
				}
			}
		}
		isCopy := atStart && dumpCopy.MatchString(line)
		if !skipStatement {
			if _, err = fmt.Fprintln(writer, line); err != nil {
				return "", err
			}
		}
		if isCopy {
			copying, skipCopy = true, skipStatement
		}
		state.consume(line)
		if !state.inStatement && state.quote == 0 && state.dollar == "" && state.block == 0 {
			skipStatement = false
		}
	}
	if err = scanner.Err(); err != nil {
		return "", err
	}
	if err = writer.Flush(); err != nil {
		return "", err
	}
	if err = output.Close(); err != nil {
		return "", err
	}
	return output.Name(), nil
}
