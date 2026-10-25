package main

// A small reader for the Lua tables WoW writes to SavedVariables files.
// It handles the subset WoW emits: strings, numbers, booleans, nil,
// nested tables with [key] = value, name = value, and positional entries.

import (
	"fmt"
	"strconv"
	"strings"
)

type luaParser struct {
	s   string
	pos int
}

func ParseSavedVariables(src string) (map[string]any, error) {
	p := &luaParser{s: src}
	out := map[string]any{}
	for {
		p.skip()
		if p.pos >= len(p.s) {
			return out, nil
		}
		name := p.ident()
		if name == "" {
			return out, p.errf("expected a variable name")
		}
		p.skip()
		if !p.consume('=') {
			return out, p.errf("expected '=' after %s", name)
		}
		v, err := p.value()
		if err != nil {
			return out, err
		}
		out[name] = v
	}
}

func (p *luaParser) errf(format string, a ...any) error {
	return fmt.Errorf("lua parse at %d: %s", p.pos, fmt.Sprintf(format, a...))
}

func (p *luaParser) skip() {
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' {
			p.pos++
			continue
		}
		if strings.HasPrefix(p.s[p.pos:], "--") {
			p.pos += 2
			if lvl, ok := p.longBracket(); ok {
				end := "]" + strings.Repeat("=", lvl) + "]"
				i := strings.Index(p.s[p.pos:], end)
				if i < 0 {
					p.pos = len(p.s)
				} else {
					p.pos += i + len(end)
				}
				continue
			}
			for p.pos < len(p.s) && p.s[p.pos] != '\n' {
				p.pos++
			}
			continue
		}
		return
	}
}

// longBracket checks for [[ or [==[ at pos and consumes it.
func (p *luaParser) longBracket() (int, bool) {
	if p.pos >= len(p.s) || p.s[p.pos] != '[' {
		return 0, false
	}
	j := p.pos + 1
	lvl := 0
	for j < len(p.s) && p.s[j] == '=' {
		lvl++
		j++
	}
	if j < len(p.s) && p.s[j] == '[' {
		p.pos = j + 1
		return lvl, true
	}
	return 0, false
}

func (p *luaParser) consume(c byte) bool {
	if p.pos < len(p.s) && p.s[p.pos] == c {
		p.pos++
		return true
	}
	return false
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func (p *luaParser) ident() string {
	start := p.pos
	if p.pos < len(p.s) && isIdentStart(p.s[p.pos]) {
		p.pos++
		for p.pos < len(p.s) && (isIdentStart(p.s[p.pos]) || (p.s[p.pos] >= '0' && p.s[p.pos] <= '9')) {
			p.pos++
		}
	}
	return p.s[start:p.pos]
}

func (p *luaParser) value() (any, error) {
	p.skip()
	if p.pos >= len(p.s) {
		return nil, p.errf("unexpected end of file")
	}
	c := p.s[p.pos]
	switch {
	case c == '{':
		return p.table()
	case c == '"' || c == '\'':
		return p.str()
	case c == '[':
		if lvl, ok := p.longBracket(); ok {
			end := "]" + strings.Repeat("=", lvl) + "]"
			i := strings.Index(p.s[p.pos:], end)
			if i < 0 {
				return nil, p.errf("unterminated long string")
			}
			v := p.s[p.pos : p.pos+i]
			p.pos += i + len(end)
			return strings.TrimPrefix(v, "\n"), nil
		}
		return nil, p.errf("unexpected '['")
	case c == '-' || c == '.' || (c >= '0' && c <= '9'):
		return p.number()
	case isIdentStart(c):
		w := p.ident()
		switch w {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "nil":
			return nil, nil
		}
		return nil, p.errf("unexpected word %q", w)
	}
	return nil, p.errf("unexpected character %q", c)
}

func (p *luaParser) number() (any, error) {
	start := p.pos
	if p.s[p.pos] == '-' {
		p.pos++
	}
	if strings.HasPrefix(strings.ToLower(p.s[p.pos:]), "0x") {
		p.pos += 2
		for p.pos < len(p.s) && strings.ContainsRune("0123456789abcdefABCDEF", rune(p.s[p.pos])) {
			p.pos++
		}
		n, err := strconv.ParseInt(strings.Replace(p.s[start:p.pos], "0x", "", 1), 16, 64)
		if err != nil {
			return nil, p.errf("bad hex number")
		}
		return float64(n), nil
	}
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' {
			p.pos++
			continue
		}
		if (c == '+' || c == '-') && (p.s[p.pos-1] == 'e' || p.s[p.pos-1] == 'E') {
			p.pos++
			continue
		}
		break
	}
	raw := p.s[start:p.pos]
	switch raw {
	case "inf", "-inf":
		return 0.0, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, p.errf("bad number %q", raw)
	}
	return f, nil
}

func (p *luaParser) str() (string, error) {
	q := p.s[p.pos]
	p.pos++
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == q {
			p.pos++
			return b.String(), nil
		}
		if c == '\\' && p.pos+1 < len(p.s) {
			p.pos++
			e := p.s[p.pos]
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\n':
				b.WriteByte('\n')
			case '\\', '"', '\'':
				b.WriteByte(e)
			default:
				if e >= '0' && e <= '9' {
					j := p.pos
					for j < len(p.s) && j < p.pos+3 && p.s[j] >= '0' && p.s[j] <= '9' {
						j++
					}
					n, _ := strconv.Atoi(p.s[p.pos:j])
					b.WriteByte(byte(n))
					p.pos = j
					continue
				}
				b.WriteByte(e)
			}
			p.pos++
			continue
		}
		b.WriteByte(c)
		p.pos++
	}
	return "", p.errf("unterminated string")
}

func (p *luaParser) table() (any, error) {
	p.pos++ // {
	m := map[string]any{}
	var seq []any
	next := 1
	for {
		p.skip()
		if p.pos >= len(p.s) {
			return nil, p.errf("unterminated table")
		}
		if p.consume('}') {
			break
		}
		var key string
		hasKey := false
		if p.s[p.pos] == '[' {
			if _, ok := p.longBracket(); ok {
				return nil, p.errf("long string as key not supported")
			}
			p.pos++
			k, err := p.value()
			if err != nil {
				return nil, err
			}
			p.skip()
			if !p.consume(']') {
				return nil, p.errf("expected ']'")
			}
			p.skip()
			if !p.consume('=') {
				return nil, p.errf("expected '='")
			}
			key = keyString(k)
			hasKey = true
		} else if isIdentStart(p.s[p.pos]) {
			save := p.pos
			w := p.ident()
			p.skip()
			if w != "true" && w != "false" && w != "nil" && p.consume('=') {
				key = w
				hasKey = true
			} else {
				p.pos = save
			}
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		if !hasKey {
			key = strconv.Itoa(next)
			next++
		}
		if v != nil {
			m[key] = v
		}
		p.skip()
		if !p.consume(',') {
			p.consume(';')
		}
	}
	// Turn 1..n integer keys into a list.
	if len(m) > 0 {
		isSeq := true
		for i := 1; i <= len(m); i++ {
			if _, ok := m[strconv.Itoa(i)]; !ok {
				isSeq = false
				break
			}
		}
		if isSeq {
			seq = make([]any, len(m))
			for i := 1; i <= len(m); i++ {
				seq[i-1] = m[strconv.Itoa(i)]
			}
			return seq, nil
		}
	}
	return m, nil
}

func keyString(k any) string {
	switch v := k.(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return fmt.Sprint(k)
}
