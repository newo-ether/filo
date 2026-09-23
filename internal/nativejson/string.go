package nativejson

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// SurrogateText preserves escaped lone UTF-16 surrogates from native JSON. Go's
// standard decoder replaces them; native snapshots and patch offsets cannot do so.
// Its internal string uses WTF-8 only for those unpaired code units.
type SurrogateText string

func Text(value any) string {
	text, _ := AsText(value)
	return text
}

func AsText(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case SurrogateText:
		return string(value), true
	default:
		return "", false
	}
}

func (s *SurrogateText) UnmarshalJSON(data []byte) error {
	value, _, err := Read(bytes.NewReader(data), false)
	if err != nil {
		return err
	}
	text, ok := AsText(value)
	if !ok {
		return errors.New("expected JSON text")
	}
	*s = SurrogateText(text)
	return nil
}

func (s SurrogateText) MarshalJSON() ([]byte, error) {
	var result strings.Builder
	result.WriteByte('"')
	plain := string(s)
	for i := 0; i < len(plain); {
		if unit, ok := surrogateAt(plain, i); ok {
			if low, pair := surrogateAt(plain, i+3); unit <= 0xdbff && pair && low >= 0xdc00 {
				result.WriteRune(utf16.DecodeRune(rune(unit), rune(low)))
				i += 6
				continue
			}
			fmt.Fprintf(&result, "\\u%04x", unit)
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(plain[i:])
		switch r {
		case '"', '\\':
			result.WriteByte('\\')
			result.WriteRune(r)
		case '\b':
			result.WriteString(`\b`)
		case '\f':
			result.WriteString(`\f`)
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&result, "\\u%04x", r)
			} else {
				result.WriteRune(r)
			}
		}
		i += size
	}
	result.WriteByte('"')
	return []byte(result.String()), nil
}

func surrogateAt(s string, i int) (uint16, bool) {
	if i+2 >= len(s) || s[i] != 0xed || s[i+1] < 0xa0 || s[i+1] > 0xbf || s[i+2]&0xc0 != 0x80 {
		return 0, false
	}
	return uint16(s[i]&15)<<12 | uint16(s[i+1]&63)<<6 | uint16(s[i+2]&63), true
}

func writeUnit(out *strings.Builder, unit uint16) {
	if unit >= 0xd800 && unit <= 0xdfff {
		out.WriteByte(byte(0xe0 | unit>>12))
		out.WriteByte(byte(0x80 | (unit>>6)&63))
		out.WriteByte(byte(0x80 | unit&63))
	} else {
		out.WriteRune(rune(unit))
	}
}

func (d *decoder) string(limit int, shared *budget, key bool) (any, error) {
	var out strings.Builder
	var pending uint16
	units, truncated, unpaired := 0, false, false
	emit := func(unit uint16) error {
		if limit >= 0 && units >= limit {
			if key {
				return errors.New("desktop JSON key/number limit")
			}
			truncated = true
			return nil
		}
		if err := d.charge(2); err != nil {
			return err
		}
		units++
		if shared != nil {
			shared.remaining--
		}
		if pending != 0 {
			if unit >= 0xdc00 && unit <= 0xdfff {
				out.WriteRune(utf16.DecodeRune(rune(pending), rune(unit)))
				pending = 0
				return nil
			}
			writeUnit(&out, pending)
			unpaired = true
			pending = 0
		}
		if unit >= 0xd800 && unit <= 0xdbff {
			pending = unit
		} else {
			writeUnit(&out, unit)
			if unit >= 0xdc00 && unit <= 0xdfff {
				unpaired = true
			}
		}
		return nil
	}
	for {
		b, err := d.r.ReadByte()
		if err != nil {
			return nil, errors.New("incomplete desktop JSON string")
		}
		if b == '"' {
			break
		}
		if b < 0x20 {
			return nil, errors.New("invalid desktop JSON control character")
		}
		if b == '\\' {
			escaped, err := d.r.ReadByte()
			if err != nil {
				return nil, errors.New("incomplete desktop JSON escape")
			}
			var unit uint16
			switch escaped {
			case '"', '\\', '/':
				unit = uint16(escaped)
			case 'b':
				unit = '\b'
			case 'f':
				unit = '\f'
			case 'n':
				unit = '\n'
			case 'r':
				unit = '\r'
			case 't':
				unit = '\t'
			case 'u':
				for range 4 {
					digit, err := d.r.ReadByte()
					if err != nil {
						return nil, errors.New("incomplete desktop JSON Unicode escape")
					}
					n := byte(0)
					switch {
					case digit >= '0' && digit <= '9':
						n = digit - '0'
					case digit >= 'a' && digit <= 'f':
						n = digit - 'a' + 10
					case digit >= 'A' && digit <= 'F':
						n = digit - 'A' + 10
					default:
						return nil, errors.New("invalid desktop JSON Unicode escape")
					}
					unit = unit<<4 | uint16(n)
				}
			default:
				return nil, errors.New("invalid desktop JSON escape")
			}
			if err := emit(unit); err != nil {
				return nil, err
			}
		} else if b < utf8.RuneSelf {
			if err := emit(uint16(b)); err != nil {
				return nil, err
			}
		} else {
			if err := d.r.UnreadByte(); err != nil {
				return nil, err
			}
			r, _, err := d.r.ReadRune()
			if err != nil {
				return nil, err
			}
			if r <= 0xffff {
				if err := emit(uint16(r)); err != nil {
					return nil, err
				}
			} else {
				hi, lo := utf16.EncodeRune(r)
				if err := emit(uint16(hi)); err != nil {
					return nil, err
				}
				if err := emit(uint16(lo)); err != nil {
					return nil, err
				}
			}
		}
	}
	mark := truncated && shared != nil && !shared.marked
	if pending != 0 && !mark {
		writeUnit(&out, pending)
		unpaired = true
	}
	if mark {
		out.WriteString(PreviewSuffix)
		shared.marked = true
		if err := d.charge(len(PreviewSuffix) * 2); err != nil {
			return nil, err
		}
	}
	if unpaired {
		return SurrogateText(out.String()), nil
	}
	return out.String(), nil
}
