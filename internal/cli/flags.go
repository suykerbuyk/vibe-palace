package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// FlagValues holds parsed flag values and positional arguments.
type FlagValues struct {
	flags map[string]string
	bools map[string]bool
	args  []string
}

// Get returns the string value of a flag, or "" if unset.
func (f *FlagValues) Get(name string) string {
	return f.flags[name]
}

// Bool returns true if a boolean flag was set.
func (f *FlagValues) Bool(name string) bool {
	return f.bools[name]
}

// Int returns the integer value of a flag, or 0 if unset or unparseable.
func (f *FlagValues) Int(name string) int {
	v, _ := strconv.Atoi(f.flags[name])
	return v
}

// Args returns positional (non-flag) arguments.
func (f *FlagValues) Args() []string {
	return f.args
}

// IsSet reports whether the flag was explicitly present on the command
// line (regardless of its value). Distinguishes "unset" from "set to
// empty string". For bool flags, equivalent to Bool(name).
func (f *FlagValues) IsSet(name string) bool {
	if _, ok := f.flags[name]; ok {
		return true
	}
	_, ok := f.bools[name]
	return ok
}

// ParseFlags parses command-line arguments against a set of flag definitions.
// It supports --flag value, --flag=value, and -f value (short form).
// Unknown flags return an error.
func ParseFlags(defs []FlagDef, args []string) (*FlagValues, error) {
	// Build lookup maps.
	longMap := make(map[string]*FlagDef, len(defs))
	shortMap := make(map[string]*FlagDef, len(defs))
	for i := range defs {
		d := &defs[i]
		longMap[d.Name] = d
		if d.Short != "" {
			shortMap[d.Short] = d
		}
	}

	fv := &FlagValues{
		flags: make(map[string]string),
		bools: make(map[string]bool),
	}

	for i := 0; i < len(args); i++ {
		a := args[i]

		// Stop parsing flags after "--".
		if a == "--" {
			fv.args = append(fv.args, args[i+1:]...)
			break
		}

		// Not a flag — positional argument.
		if !strings.HasPrefix(a, "-") {
			fv.args = append(fv.args, a)
			continue
		}

		// Handle --flag=value.
		var key, val string
		var hasEq bool
		if eqIdx := strings.IndexByte(a, '='); eqIdx >= 0 {
			key = a[:eqIdx]
			val = a[eqIdx+1:]
			hasEq = true
		} else {
			key = a
		}

		// Resolve to definition.
		def := longMap[key]
		if def == nil {
			def = shortMap[key]
		}
		if def == nil {
			return nil, fmt.Errorf("unknown flag: %s", key)
		}

		if def.IsBool() {
			if hasEq {
				// --bool=true / --bool=false
				switch strings.ToLower(val) {
				case "true", "1", "yes":
					fv.bools[def.Name] = true
				case "false", "0", "no":
					fv.bools[def.Name] = false
				default:
					return nil, fmt.Errorf("invalid boolean value for %s: %s", def.Name, val)
				}
			} else {
				fv.bools[def.Name] = true
			}
		} else {
			if hasEq {
				fv.flags[def.Name] = val
			} else if i+1 < len(args) {
				i++
				fv.flags[def.Name] = args[i]
			} else {
				return nil, fmt.Errorf("flag %s requires a value", key)
			}
		}
	}

	return fv, nil
}
