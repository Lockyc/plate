// Package cli holds the flag and error conventions every plate command
// shares: flags before positional arguments, exit 2 for a usage error, exit
// 1 for a failed operation, errors printed as "plate <op>: <reason>".
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Flags makes the flag set for "plate <op>".
func Flags(op, usage string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("plate "+op, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: plate %s\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

// Float defines a float flag that refuses NaN and the infinities, which
// flag.Float64 accepts and which slip past every range check.
func Float(fs *flag.FlagSet, name string, value float64, usage string) *float64 {
	p := new(float64)
	*p = value
	fs.Var((*finite)(p), name, usage)
	return p
}

type finite float64

func (f *finite) String() string { return strconv.FormatFloat(float64(*f), 'g', -1, 64) }

func (f *finite) Set(s string) error {
	v, ok := Finite(s)
	if !ok {
		return errors.New("want a finite number")
	}
	*f = finite(v)
	return nil
}

// Int defines an int flag read in base 10 only. flag.Int reads base 0, so
// "0150" is octal 104 and "0x10" is 16: a zero-padded number is silently
// another number.
func Int(fs *flag.FlagSet, name string, value int, usage string) *int {
	p := new(int)
	*p = value
	fs.Var((*decimal)(p), name, usage)
	return p
}

type decimal int

func (d *decimal) String() string { return strconv.Itoa(int(*d)) }

func (d *decimal) Set(s string) error {
	v, err := strconv.ParseInt(s, 10, strconv.IntSize)
	if err != nil {
		return errors.New("want a whole number")
	}
	*d = decimal(v)
	return nil
}

// Multi is a repeatable string flag: each use appends its value.
type Multi []string

func (m *Multi) String() string     { return strings.Join(*m, ", ") }
func (m *Multi) Set(s string) error { *m = append(*m, s); return nil }

// Finite parses s as a number, refusing NaN and the infinities, which
// ParseFloat accepts.
func Finite(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// Parse parses args and checks the positional count (any count when
// positional < 0). When ok is false the caller returns code.
func Parse(fs *flag.FlagSet, args []string, positional int) (rest []string, code int, ok bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, 0, false
		}
		return nil, 2, false
	}
	if positional >= 0 && fs.NArg() != positional {
		fs.Usage()
		return nil, 2, false
	}
	return fs.Args(), 0, true
}

// VerbUsageCode is the exit status for a command with verbs that printed
// its usage given args: 0 when they asked for help, as Parse returns, else 2.
func VerbUsageCode(args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
		return 0
	}
	return 2
}

// Usage reports a usage error for op and returns exit status 2.
func Usage(stderr io.Writer, op, format string, a ...any) int {
	fmt.Fprintf(stderr, "plate %s: %s\n", op, fmt.Sprintf(format, a...))
	return 2
}

// Fail reports err for op and returns exit status 1.
func Fail(stderr io.Writer, op string, err error) int {
	fmt.Fprintf(stderr, "plate %s: %v\n", op, err)
	return 1
}
