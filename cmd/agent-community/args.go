package main

import (
	"flag"
	"strings"
)

// parse calls fs.Parse after reordering args so flags can appear in any
// position relative to positionals. Centralizing this means every cmd_*.go
// gets the same arg-order tolerance.
func parse(fs *flag.FlagSet, args []string) error {
	return fs.Parse(reorderArgs(fs, args))
}

// reorderArgs splits args into flag-pieces and positional-pieces according to
// the given flag set, then returns flag-pieces followed by positionals.
//
// Stdlib flag.Parse stops at the first positional arg, which breaks
// invocations like `init my-community --theme cheeses` (the user expects
// either order to work). This helper makes both orders valid by ensuring
// flags come first when fed to flag.Parse.
//
// Recognizes:
//   - "--flag=val" / "-flag=val" — single token, kept as-is on the flag side
//   - "--flag val" / "-flag val" — two tokens; both go to flag side
//   - "--" — terminator; everything after goes to positional, in order
//   - bare "--" with no following arg — kept as terminator
//
// Bool flags don't consume the next arg; the lookup-based check below knows
// which flags expect values.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	var flagsOut, positional []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if isFlagToken(a) {
			name, hasEq := parseFlagName(a)
			flagsOut = append(flagsOut, a)
			if !hasEq && expectsValue(fs, name) && i+1 < len(args) {
				flagsOut = append(flagsOut, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}
		positional = append(positional, a)
		i++
	}
	return append(flagsOut, positional...)
}

func isFlagToken(a string) bool {
	if len(a) < 2 || a[0] != '-' {
		return false
	}
	if a == "-" {
		return false
	}
	return true
}

// parseFlagName returns the flag name (without leading dashes) and whether
// the token carries an "=value" suffix.
func parseFlagName(a string) (string, bool) {
	s := strings.TrimLeft(a, "-")
	if i := strings.IndexByte(s, '='); i >= 0 {
		return s[:i], true
	}
	return s, false
}

// expectsValue returns true if the named flag is registered on fs and is not
// a boolean (booleans don't consume the following token).
func expectsValue(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
		return false
	}
	return true
}
