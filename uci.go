// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

// uci.go — tiny reader for OpenWrt UCI config files.
//
// We parse /etc/config/* directly instead of shelling out to `uci`. Reasons:
//   - no exec, no dependency on the uci binary being present/working
//   - deterministic: `uci show` output quoting has changed across releases
//   - ksd only ever *reads* config, so a read-only parser is enough
//
// Supported grammar (the subset UCI actually emits):
//
//	config <type> ['<name>']
//	    option <key> '<value>'
//	    list   <key> '<value>'
//	# comments

import (
	"bufio"
	"os"
	"strings"
)

// Section is one `config` block. Options holds scalars, Lists holds repeated
// values (UCI `list` and repeated `option` with the same key both land here).
type Section struct {
	Type    string
	Name    string
	Options map[string]string
	Lists   map[string][]string
}

// UCIFile is a parsed config file: indexed by "type" and "type.name".
type UCIFile struct {
	Sections []*Section
	byKey    map[string]*Section
}

func (u *UCIFile) Get(typeName string) *Section {
	if u == nil {
		return nil
	}
	return u.byKey[typeName]
}

// Lookup finds a section by type, or by type.name.
func (u *UCIFile) Lookup(typeName, name string) *Section {
	if u == nil {
		return nil
	}
	if name != "" {
		if s, ok := u.byKey[typeName+"."+name]; ok {
			return s
		}
	}
	return u.byKey[typeName]
}

func LoadUCI(path string) (*UCIFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	u := &UCIFile{byKey: map[string]*Section{}}
	var cur *Section

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := splitFields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "config":
			if len(fields) < 2 {
				continue
			}
			cur = &Section{
				Type:    unquote(fields[1]),
				Options: map[string]string{},
				Lists:   map[string][]string{},
			}
			if len(fields) > 2 {
				cur.Name = unquote(fields[2])
			}
			u.Sections = append(u.Sections, cur)
			// First section of a given type wins for Get(type); anonymous
			// sections ("@defaults[0]") have no name, so type alone is the key.
			if _, exists := u.byKey[cur.Type]; !exists {
				u.byKey[cur.Type] = cur
			}
			if cur.Name != "" {
				key := cur.Type + "." + cur.Name
				if _, exists := u.byKey[key]; !exists {
					u.byKey[key] = cur
				}
			}

		case "option", "list":
			if cur == nil || len(fields) < 3 {
				continue
			}
			k := unquote(fields[1])
			v := unquote(strings.Join(fields[2:], " "))
			if fields[0] == "list" {
				cur.Lists[k] = append(cur.Lists[k], v)
			} else {
				// A repeated `option` with the same key is treated as a list
				// entry as well — matches how `uci show` renders UCI lists.
				if _, seen := cur.Options[k]; seen {
					cur.Lists[k] = append(cur.Lists[k], v)
				} else {
					cur.Options[k] = v
				}
			}
		}
	}
	return u, sc.Err()
}

// unquote strips one layer of '...' or "...".
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// splitFields splits on whitespace but keeps quoted spans intact, so that
// option ntp_servers 'a b c' parses as a single value.
func splitFields(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := byte(0)
	started := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQ != 0:
			cur.WriteByte(c)
			if c == inQ {
				inQ = 0
			}
		case c == '\'' || c == '"':
			inQ = c
			cur.WriteByte(c)
			started = true
		case c == ' ' || c == '\t':
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if started {
		out = append(out, cur.String())
	}
	return out
}
