package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadDir reads every *.sql file in dir (sorted by name) and parses the union of
// their CREATE TABLE / CREATE TYPE … AS ENUM statements into one desired schema.
// schemaName is the target schema (e.g. "public"); it labels the result and is
// stripped from any schema-qualified table/type names so they compare against an
// introspection of that schema.
//
// Unsupported or unrecognised statements are collected in Warnings rather than
// failing the parse — the declarative flow is best-effort over a subset of DDL
// (see Diff for the covered surface).
func LoadDir(dir, schemaName string) (*Schema, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read schema dir %q: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".sql") {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)

	out := &Schema{Name: schemaName}
	var warnings []string
	for _, name := range files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, err
		}
		w, err := parseInto(out, string(b), schemaName)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, warn := range w {
			warnings = append(warnings, name+": "+warn)
		}
	}
	return out, warnings, nil
}

// Parse parses a single SQL string into a Schema. It is the file-free entry
// point used by tests.
func Parse(sql, schemaName string) (*Schema, []string, error) {
	out := &Schema{Name: schemaName}
	w, err := parseInto(out, sql, schemaName)
	return out, w, err
}

func parseInto(out *Schema, sql, schemaName string) ([]string, error) {
	var warnings []string
	for _, stmt := range splitStatements(sql) {
		raw := strings.TrimSpace(stmt)
		if raw == "" {
			continue
		}
		head := strings.ToLower(collapseSpaces(raw))
		switch {
		case strings.HasPrefix(head, "create table"):
			t, err := parseCreateTable(raw, schemaName)
			if err != nil {
				return warnings, err
			}
			if t == nil { // e.g. CREATE TABLE ... AS SELECT (unsupported)
				warnings = append(warnings, "skipped unsupported CREATE TABLE form")
				continue
			}
			if _, dup := out.table(t.Name); dup {
				return warnings, fmt.Errorf("table %q declared more than once", t.Name)
			}
			out.Tables = append(out.Tables, *t)
		case strings.HasPrefix(head, "create type"):
			e, ok := parseCreateEnum(raw, schemaName)
			if !ok {
				warnings = append(warnings, "skipped CREATE TYPE that is not AS ENUM")
				continue
			}
			if _, dup := out.enum(e.Name); dup {
				return warnings, fmt.Errorf("enum %q declared more than once", e.Name)
			}
			out.Enums = append(out.Enums, *e)
		default:
			warnings = append(warnings, "skipped statement: "+firstWords(raw, 4))
		}
	}
	return warnings, nil
}

// splitStatements splits a SQL string on top-level semicolons, ignoring
// semicolons inside single-quoted strings, double-quoted identifiers, line
// comments (-- …) and block comments (/* … */), and inside parentheses.
func splitStatements(sql string) []string {
	var stmts []string
	var cur strings.Builder
	depth := 0
	i := 0
	for i < len(sql) {
		c := sql[i]
		switch {
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			i += 2
			for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i += 2
		case c == '\'':
			cur.WriteByte(c)
			i++
			for i < len(sql) {
				cur.WriteByte(sql[i])
				if sql[i] == '\'' {
					// doubled '' is an escaped quote, stay in string
					if i+1 < len(sql) && sql[i+1] == '\'' {
						cur.WriteByte(sql[i+1])
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '"':
			cur.WriteByte(c)
			i++
			for i < len(sql) {
				cur.WriteByte(sql[i])
				if sql[i] == '"' {
					i++
					break
				}
				i++
			}
		case c == '(':
			depth++
			cur.WriteByte(c)
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			cur.WriteByte(c)
			i++
		case c == ';' && depth == 0:
			stmts = append(stmts, cur.String())
			cur.Reset()
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		stmts = append(stmts, cur.String())
	}
	return stmts
}

// parseCreateTable parses `CREATE TABLE [IF NOT EXISTS] name (body)`. It returns
// (nil, nil) for forms it does not model (e.g. CREATE TABLE … AS SELECT, or a
// PARTITION OF). Table-level constraints inside the body are skipped.
func parseCreateTable(raw, schemaName string) (*Table, error) {
	open := strings.IndexByte(raw, '(')
	if open < 0 {
		return nil, nil // no column list (AS SELECT, PARTITION OF, …)
	}
	header := collapseSpaces(raw[:open])
	// header like: CREATE TABLE [IF NOT EXISTS] [schema.]name
	name := header
	for _, p := range []string{"create table", "if not exists"} {
		name = strings.TrimSpace(trimPrefixFold(name, p))
	}
	name = unqualify(stripQuotes(strings.TrimSpace(name)), schemaName)
	if name == "" {
		return nil, fmt.Errorf("could not parse table name from %q", firstWords(raw, 6))
	}

	close := matchingParen(raw, open)
	if close < 0 {
		return nil, fmt.Errorf("unbalanced parentheses in CREATE TABLE %q", name)
	}
	body := raw[open+1 : close]

	t := &Table{Name: name, Raw: strings.TrimSpace(raw)}
	for _, item := range splitTopLevel(body, ',') {
		item = strings.TrimSpace(item)
		if item == "" || isTableConstraint(item) {
			continue
		}
		col, ok := parseColumn(item)
		if ok {
			t.Columns = append(t.Columns, col)
		}
	}
	return t, nil
}

// isTableConstraint reports whether a body item is a table-level constraint
// (which the diff does not model) rather than a column definition.
func isTableConstraint(item string) bool {
	first := strings.ToUpper(firstToken(item))
	switch first {
	case "PRIMARY", "FOREIGN", "UNIQUE", "CHECK", "CONSTRAINT", "EXCLUDE", "LIKE":
		return true
	}
	return false
}

// parseColumn parses one `name type [constraints…]` column definition.
func parseColumn(item string) (Column, bool) {
	toks := tokenize(item)
	if len(toks) < 2 {
		return Column{}, false
	}
	col := Column{Name: stripQuotes(toks[0]), Nullable: true, Raw: strings.TrimSpace(item)}

	// Consume type tokens until a column-constraint keyword appears.
	var typeToks []string
	i := 1
	for ; i < len(toks); i++ {
		if isConstraintKeyword(toks[i]) && len(typeToks) > 0 {
			break
		}
		typeToks = append(typeToks, toks[i])
	}
	col.UDT = normaliseType(strings.Join(typeToks, " "))

	rest := strings.ToUpper(collapseSpaces(strings.Join(toks[i:], " ")))
	if serialTypes[strings.ToLower(baseTypeName(strings.Join(typeToks, " ")))] {
		col.HasDefault = true
	}
	if strings.Contains(rest, "DEFAULT ") || strings.HasSuffix(rest, "DEFAULT") {
		col.HasDefault = true
	}
	if strings.Contains(rest, "GENERATED ") && strings.Contains(rest, "IDENTITY") {
		col.IsIdentity = true
		col.HasDefault = true
		col.Nullable = false // an identity column is implicitly NOT NULL
	}
	if strings.Contains(rest, "NOT NULL") || strings.Contains(rest, "PRIMARY KEY") {
		col.Nullable = false
	}
	return col, true
}

// normaliseType maps a declared type spelling to a udt_name, handling arrays
// (trailing [] or `ARRAY`) and stripping length/precision modifiers.
func normaliseType(t string) string {
	t = strings.TrimSpace(t)
	isArray := false
	up := strings.ToUpper(t)
	if strings.HasSuffix(strings.TrimSpace(up), "ARRAY") {
		isArray = true
		t = strings.TrimSpace(t[:len(t)-len("ARRAY")])
	}
	for strings.HasSuffix(strings.TrimSpace(t), "[]") {
		isArray = true
		t = strings.TrimSpace(t)
		t = strings.TrimSpace(t[:len(t)-2])
	}
	base := strings.ToLower(baseTypeName(t))
	udt, ok := typeAliases[base]
	if !ok {
		udt = base // enum type name or an unmapped type: compare verbatim
	}
	if isArray {
		return "_" + udt
	}
	return udt
}

// baseTypeName strips a trailing (…) modifier from a type, e.g. numeric(10,2) →
// numeric, varchar(255) → varchar, timestamp(3) with time zone → timestamp with
// time zone.
func baseTypeName(t string) string {
	t = collapseSpaces(strings.TrimSpace(t))
	// Remove any parenthesised modifier group.
	for {
		open := strings.IndexByte(t, '(')
		if open < 0 {
			break
		}
		close := matchingParen(t, open)
		if close < 0 {
			t = strings.TrimSpace(t[:open])
			break
		}
		t = strings.TrimSpace(t[:open] + " " + t[close+1:])
	}
	return collapseSpaces(t)
}

// parseCreateEnum parses `CREATE TYPE name AS ENUM ('a','b',…)`.
func parseCreateEnum(raw, schemaName string) (*Enum, bool) {
	up := strings.ToUpper(collapseSpaces(raw))
	asEnum := strings.Index(up, " AS ENUM")
	if asEnum < 0 {
		return nil, false
	}
	// name is between "CREATE TYPE" and "AS ENUM"
	collapsed := collapseSpaces(raw)
	nameArea := strings.TrimSpace(collapsed[len("create type"):asEnum])
	nameArea = strings.TrimSpace(trimPrefixFold(nameArea, "create type")) // safety
	name := unqualify(stripQuotes(strings.TrimSpace(nameArea)), schemaName)
	if name == "" {
		return nil, false
	}
	open := strings.IndexByte(raw, '(')
	if open < 0 {
		return &Enum{Name: name}, true
	}
	close := matchingParen(raw, open)
	if close < 0 {
		return nil, false
	}
	e := &Enum{Name: name}
	for _, lit := range splitTopLevel(raw[open+1:close], ',') {
		lit = strings.TrimSpace(lit)
		e.Labels = append(e.Labels, stripSingleQuotes(lit))
	}
	return e, true
}

// ---- small lexical helpers ----

// tokenize splits a column definition into whitespace-separated tokens, keeping
// a balanced (…) group and a '…' literal attached to the preceding token so
// numeric(10,2) and DEFAULT now() stay whole.
func tokenize(s string) []string {
	var toks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
			i++
		case c == '(':
			close := matchingParen(s, i)
			if close < 0 {
				cur.WriteString(s[i:])
				i = len(s)
			} else {
				cur.WriteString(s[i : close+1])
				i = close + 1
			}
		case c == '\'':
			j := i + 1
			for j < len(s) {
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			cur.WriteString(s[i:min(j+1, len(s))])
			i = j + 1
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				j++
			}
			cur.WriteString(s[i:min(j+1, len(s))])
			i = j + 1
		default:
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return toks
}

func isConstraintKeyword(tok string) bool {
	switch strings.ToUpper(tok) {
	case "NOT", "NULL", "DEFAULT", "PRIMARY", "UNIQUE", "REFERENCES",
		"CHECK", "GENERATED", "COLLATE", "CONSTRAINT", "KEY":
		return true
	}
	return false
}

// splitTopLevel splits s on sep at parenthesis depth 0, ignoring separators
// inside quotes.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	var cur strings.Builder
	depth := 0
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\'':
			cur.WriteByte(c)
			i++
			for i < len(s) {
				cur.WriteByte(s[i])
				if s[i] == '\'' {
					i++
					break
				}
				i++
			}
		case c == '"':
			cur.WriteByte(c)
			i++
			for i < len(s) {
				cur.WriteByte(s[i])
				if s[i] == '"' {
					i++
					break
				}
				i++
			}
		case c == '(':
			depth++
			cur.WriteByte(c)
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			cur.WriteByte(c)
			i++
		case c == sep && depth == 0:
			parts = append(parts, cur.String())
			cur.Reset()
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	parts = append(parts, cur.String())
	return parts
}

// matchingParen returns the index of the ')' that closes the '(' at open, or -1.
func matchingParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func firstToken(s string) string {
	return firstWords(s, 1)
}

func firstWords(s string, n int) string {
	f := strings.Fields(collapseSpaces(s))
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}

func trimPrefixFold(s, prefix string) string {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):]
	}
	return s
}

func stripQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func stripSingleQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	return s
}

// unqualify drops a leading schema qualifier when it matches the target schema
// (or is "public"), so `public.users` and `users` compare equal.
func unqualify(name, schemaName string) string {
	dot := strings.IndexByte(name, '.')
	if dot < 0 {
		return name
	}
	qualifier := stripQuotes(strings.TrimSpace(name[:dot]))
	rest := stripQuotes(strings.TrimSpace(name[dot+1:]))
	if qualifier == schemaName || qualifier == "public" {
		return rest
	}
	return rest
}
