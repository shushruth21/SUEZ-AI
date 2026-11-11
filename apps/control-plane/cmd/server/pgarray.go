package main

import (
	"errors"
	"fmt"
	"strings"
)

// textArray scans a one-dimensional PostgreSQL text[] (or uuid[]) value,
// which database/sql delivers in its text form: {a,b,"c d"}.
type textArray []string

func (a *textArray) Scan(src any) error {
	var raw string
	switch value := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		raw = value
	case []byte:
		raw = string(value)
	default:
		return fmt.Errorf("cannot scan %T into a text array", src)
	}
	if len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return errors.New("malformed array literal")
	}
	out := []string{}
	body := raw[1 : len(raw)-1]
	for i := 0; i < len(body); {
		var element strings.Builder
		if body[i] == '"' {
			i++
			for i < len(body) && body[i] != '"' {
				if body[i] == '\\' && i+1 < len(body) {
					i++
				}
				element.WriteByte(body[i])
				i++
			}
			if i >= len(body) {
				return errors.New("malformed array literal")
			}
			i++ // closing quote
		} else {
			for i < len(body) && body[i] != ',' {
				element.WriteByte(body[i])
				i++
			}
		}
		out = append(out, element.String())
		if i < len(body) {
			if body[i] != ',' {
				return errors.New("malformed array literal")
			}
			i++
		}
	}
	*a = out
	return nil
}
