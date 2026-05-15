package database

import (
	"strconv"
	"strings"
	"unicode"
)

// QMarkToPostgres rewrites database/sql question-mark placeholders to
// PostgreSQL positional placeholders while leaving quoted strings, quoted
// identifiers, dollar-quoted strings, and SQL comments untouched.
func QMarkToPostgres(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}

	var out strings.Builder
	out.Grow(len(query) + 8)
	placeholder := 1

	for i := 0; i < len(query); {
		ch := query[i]

		switch ch {
		case '\'':
			next := copySingleQuoted(&out, query, i)
			i = next
			continue
		case '"':
			next := copyDoubleQuoted(&out, query, i)
			i = next
			continue
		case '-':
			if i+1 < len(query) && query[i+1] == '-' {
				next := copyLineComment(&out, query, i)
				i = next
				continue
			}
		case '/':
			if i+1 < len(query) && query[i+1] == '*' {
				next := copyBlockComment(&out, query, i)
				i = next
				continue
			}
		case '$':
			if endTag, ok := dollarQuoteTag(query, i); ok {
				next := copyDollarQuoted(&out, query, i, endTag)
				i = next
				continue
			}
		case '?':
			out.WriteByte('$')
			out.WriteString(strconv.Itoa(placeholder))
			placeholder++
			i++
			continue
		}

		out.WriteByte(ch)
		i++
	}

	return out.String()
}

func copySingleQuoted(out *strings.Builder, query string, start int) int {
	out.WriteByte(query[start])
	for i := start + 1; i < len(query); i++ {
		out.WriteByte(query[i])
		if query[i] == '\'' {
			if i+1 < len(query) && query[i+1] == '\'' {
				out.WriteByte(query[i+1])
				i++
				continue
			}
			return i + 1
		}
		if query[i] == '\\' && i+1 < len(query) {
			out.WriteByte(query[i+1])
			i++
		}
	}
	return len(query)
}

func copyDoubleQuoted(out *strings.Builder, query string, start int) int {
	out.WriteByte(query[start])
	for i := start + 1; i < len(query); i++ {
		out.WriteByte(query[i])
		if query[i] == '"' {
			if i+1 < len(query) && query[i+1] == '"' {
				out.WriteByte(query[i+1])
				i++
				continue
			}
			return i + 1
		}
	}
	return len(query)
}

func copyLineComment(out *strings.Builder, query string, start int) int {
	for i := start; i < len(query); i++ {
		out.WriteByte(query[i])
		if query[i] == '\n' {
			return i + 1
		}
	}
	return len(query)
}

func copyBlockComment(out *strings.Builder, query string, start int) int {
	out.WriteString("/*")
	for i := start + 2; i < len(query); i++ {
		out.WriteByte(query[i])
		if query[i] == '*' && i+1 < len(query) && query[i+1] == '/' {
			out.WriteByte('/')
			return i + 2
		}
	}
	return len(query)
}

func dollarQuoteTag(query string, start int) (string, bool) {
	if start >= len(query) || query[start] != '$' {
		return "", false
	}
	end := start + 1
	for end < len(query) && query[end] != '$' {
		r := rune(query[end])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && query[end] != '_' {
			return "", false
		}
		end++
	}
	if end >= len(query) || query[end] != '$' {
		return "", false
	}
	return query[start : end+1], true
}

func copyDollarQuoted(out *strings.Builder, query string, start int, tag string) int {
	out.WriteString(tag)
	contentStart := start + len(tag)
	end := strings.Index(query[contentStart:], tag)
	if end < 0 {
		out.WriteString(query[contentStart:])
		return len(query)
	}
	contentEnd := contentStart + end
	out.WriteString(query[contentStart:contentEnd])
	out.WriteString(tag)
	return contentEnd + len(tag)
}
