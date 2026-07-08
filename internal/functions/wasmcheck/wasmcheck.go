// Package wasmcheck inspects a compiled WebAssembly module's imports and
// exports without a full runtime. It exists so `functions deploy` can validate
// a produced module *before* uploading, matching the platform's deploy-time
// validator (internal/functions/validate.go on the server): imports must come
// only from the "kethosbase" and "wasi_snapshot_preview1" namespaces, and the
// module must export "_start".
//
// It parses just enough of the binary format (the import and export sections)
// per the WebAssembly core spec; it is not a general Wasm parser.
package wasmcheck

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
)

// AllowedImportModules are the only host namespaces the platform provides.
var AllowedImportModules = map[string]bool{
	"kethosbase":             true,
	"wasi_snapshot_preview1": true,
}

// RequiredExport is the WASI command-pattern entrypoint the platform requires.
const RequiredExport = "_start"

// Info summarizes what we parsed out of a module.
type Info struct {
	// ImportModules is the sorted, de-duplicated set of import namespaces.
	ImportModules []string
	// Imports is every (module, name) import pair, in file order.
	Imports []Import
	// Exports is every exported name, in file order.
	Exports []string
}

// Import is a single (module, name) import entry.
type Import struct {
	Module string
	Name   string
}

// HasExport reports whether name is exported.
func (i *Info) HasExport(name string) bool {
	for _, e := range i.Exports {
		if e == name {
			return true
		}
	}
	return false
}

// Inspect parses the import and export sections of a Wasm module.
func Inspect(wasm []byte) (*Info, error) {
	if len(wasm) < 8 {
		return nil, errors.New("not a WebAssembly module: too short")
	}
	// Magic "\0asm" + version 1.
	if wasm[0] != 0x00 || wasm[1] != 0x61 || wasm[2] != 0x73 || wasm[3] != 0x6d {
		return nil, errors.New("not a WebAssembly module: bad magic")
	}
	if v := binary.LittleEndian.Uint32(wasm[4:8]); v != 1 {
		return nil, fmt.Errorf("unsupported Wasm version %d", v)
	}

	info := &Info{}
	seen := map[string]bool{}
	p := &parser{buf: wasm, pos: 8}

	for p.pos < len(p.buf) {
		secID, err := p.byte()
		if err != nil {
			return nil, err
		}
		secLen, err := p.uvarint()
		if err != nil {
			return nil, err
		}
		secStart := p.pos
		secEnd := secStart + int(secLen)
		if secEnd > len(p.buf) || secEnd < secStart {
			return nil, errors.New("malformed module: section length out of range")
		}

		switch secID {
		case 2: // import section
			if err := parseImports(p, secEnd, info, seen); err != nil {
				return nil, err
			}
		case 7: // export section
			if err := parseExports(p, secEnd, info); err != nil {
				return nil, err
			}
		}
		// Skip to the declared end regardless of what we consumed.
		p.pos = secEnd
	}

	sort.Strings(info.ImportModules)
	return info, nil
}

// Validate applies the same rules as the platform's deploy-time validator:
// every import namespace must be allowed, and RequiredExport must be present.
func Validate(wasm []byte) (*Info, error) {
	info, err := Inspect(wasm)
	if err != nil {
		return nil, err
	}
	for _, m := range info.ImportModules {
		if !AllowedImportModules[m] {
			return info, fmt.Errorf("module imports an unsupported host module %q; only kethosbase and wasi_snapshot_preview1 are available", m)
		}
	}
	if !info.HasExport(RequiredExport) {
		return info, fmt.Errorf("module has no WASI %q entrypoint", RequiredExport)
	}
	return info, nil
}

func parseImports(p *parser, end int, info *Info, seen map[string]bool) error {
	count, err := p.uvarint()
	if err != nil {
		return err
	}
	for i := uint64(0); i < count; i++ {
		mod, err := p.name()
		if err != nil {
			return err
		}
		name, err := p.name()
		if err != nil {
			return err
		}
		kind, err := p.byte()
		if err != nil {
			return err
		}
		// Skip the import descriptor payload by kind.
		switch kind {
		case 0x00: // func: typeidx
			if _, err := p.uvarint(); err != nil {
				return err
			}
		case 0x01: // table: reftype + limits
			if _, err := p.byte(); err != nil {
				return err
			}
			if err := p.skipLimits(); err != nil {
				return err
			}
		case 0x02: // mem: limits
			if err := p.skipLimits(); err != nil {
				return err
			}
		case 0x03: // global: valtype + mut
			if _, err := p.byte(); err != nil {
				return err
			}
			if _, err := p.byte(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown import kind 0x%02x", kind)
		}
		info.Imports = append(info.Imports, Import{Module: mod, Name: name})
		if !seen[mod] {
			seen[mod] = true
			info.ImportModules = append(info.ImportModules, mod)
		}
	}
	if p.pos > end {
		return errors.New("malformed import section")
	}
	return nil
}

func parseExports(p *parser, end int, info *Info) error {
	count, err := p.uvarint()
	if err != nil {
		return err
	}
	for i := uint64(0); i < count; i++ {
		name, err := p.name()
		if err != nil {
			return err
		}
		if _, err := p.byte(); err != nil { // export kind
			return err
		}
		if _, err := p.uvarint(); err != nil { // index
			return err
		}
		info.Exports = append(info.Exports, name)
	}
	if p.pos > end {
		return errors.New("malformed export section")
	}
	return nil
}

// ---- minimal LEB128 / byte cursor ----

type parser struct {
	buf []byte
	pos int
}

func (p *parser) byte() (byte, error) {
	if p.pos >= len(p.buf) {
		return 0, io.ErrUnexpectedEOF
	}
	b := p.buf[p.pos]
	p.pos++
	return b, nil
}

// uvarint reads an unsigned LEB128 integer.
func (p *parser) uvarint() (uint64, error) {
	var result uint64
	var shift uint
	for {
		b, err := p.byte()
		if err != nil {
			return 0, err
		}
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, errors.New("LEB128 overflow")
		}
	}
}

// name reads a length-prefixed UTF-8 name.
func (p *parser) name() (string, error) {
	n, err := p.uvarint()
	if err != nil {
		return "", err
	}
	if p.pos+int(n) > len(p.buf) {
		return "", io.ErrUnexpectedEOF
	}
	s := string(p.buf[p.pos : p.pos+int(n)])
	p.pos += int(n)
	return s, nil
}

// skipLimits consumes a limits record (flags + min [+ max]).
func (p *parser) skipLimits() error {
	flags, err := p.byte()
	if err != nil {
		return err
	}
	if _, err := p.uvarint(); err != nil { // min
		return err
	}
	if flags&0x01 != 0 { // has max
		if _, err := p.uvarint(); err != nil {
			return err
		}
	}
	return nil
}
