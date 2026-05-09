// Tiny flag parser. We don't pull cobra/pflag — keeps the binary small and
// lets us do GNU-style "--flag value" alongside positional args without faff.
//
// Each subcommand calls parseFlags(args, &out) where out has fields tagged
// by name, OR uses the lower-level flagSet that supports repeated flags
// (needed for `--attach foo --attach bar`).
package main

import (
	"fmt"
	"strconv"
	"strings"
)

// flagSet collects flag values during a parse. Repeated flags accumulate
// into the slice variant.
type flagSet struct {
	str    map[string]*string
	strN   map[string]*[]string
	intF   map[string]*int
	bool   map[string]*bool
	pos    []string
}

func newFlagSet() *flagSet {
	return &flagSet{
		str:  map[string]*string{},
		strN: map[string]*[]string{},
		intF: map[string]*int{},
		bool: map[string]*bool{},
	}
}

func (f *flagSet) String(name string, def string) *string {
	v := def
	f.str[name] = &v
	return &v
}

// StringSlice supports repeated --name foo --name bar flags.
func (f *flagSet) StringSlice(name string) *[]string {
	v := []string{}
	f.strN[name] = &v
	return &v
}

func (f *flagSet) Int(name string, def int) *int {
	v := def
	f.intF[name] = &v
	return &v
}

func (f *flagSet) Bool(name string) *bool {
	v := false
	f.bool[name] = &v
	return &v
}

// Parse walks args. Anything that isn't a recognised --flag becomes a
// positional. Returns positional args.
func (f *flagSet) Parse(args []string) ([]string, error) {
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			f.pos = append(f.pos, a)
			i++
			continue
		}
		// Strip leading --
		name := strings.TrimPrefix(a, "--")
		// Support --flag=value
		var inlineVal string
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			inlineVal = name[eq+1:]
			name = name[:eq]
		}

		if p, ok := f.bool[name]; ok {
			*p = true
			i++
			continue
		}

		// All other flags need a value.
		var val string
		if inlineVal != "" {
			val = inlineVal
		} else {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--%s requires a value", name)
			}
			val = args[i+1]
			i++
		}
		i++

		switch {
		case f.str[name] != nil:
			*f.str[name] = val
		case f.strN[name] != nil:
			*f.strN[name] = append(*f.strN[name], val)
		case f.intF[name] != nil:
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("--%s expects integer, got %q", name, val)
			}
			*f.intF[name] = n
		default:
			return nil, fmt.Errorf("unknown flag --%s", name)
		}
	}
	return f.pos, nil
}
