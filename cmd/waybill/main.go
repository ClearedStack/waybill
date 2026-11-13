// Command waybill turns one approved record into a credit note and a build
// specification.
//
//	waybill compile [-out DIR] [FILE]
//
// The record is read from FILE, or from standard input when FILE is absent
// or "-". The result is printed as JSON. With -out, the credit note and the
// build specification are also written to DIR.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/clearedstack/waybill/internal/buildspec"
	"github.com/clearedstack/waybill/internal/compile"
	"github.com/clearedstack/waybill/internal/fault"
	"github.com/clearedstack/waybill/internal/record"
)

// Version is the version in component.json.
const Version = "v0.1.0-draft"

// Exit codes.
const (
	exitOK       = 0
	exitFault    = 1 // one of the three public errors; see the printed result
	exitUsage    = 2
	exitInternal = 3
)

const usage = `usage:
  waybill compile [-out DIR] [FILE]   compile one approved record (FILE or standard input)
  waybill version                     print the version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(stdout, "waybill", Version)
		return exitOK
	}
	if len(args) == 0 || args[0] != "compile" {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	flags := flag.NewFlagSet("compile", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	outDir := flags.String("out", "", "")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() > 1 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	var res compile.Result
	if data, ok := read(flags.Arg(0), stdin); ok {
		res = compile.Compile(data)
	} else {
		res = compile.Result{Error: fault.Broken("", "the record could not be read")}
	}

	out, err := buildspec.Encode(res)
	if err != nil {
		fmt.Fprintln(stderr, "waybill: the result could not be encoded")
		return exitInternal
	}
	if res.Error == nil && *outDir != "" {
		if !write(*outDir, res) {
			fmt.Fprintln(stderr, "waybill: the output files could not be written")
			return exitInternal
		}
	}
	stdout.Write(out)
	if res.Error != nil {
		return exitFault
	}
	return exitOK
}

// read returns at most one byte more than the record limit, so an oversized
// record is refused without being held in memory.
func read(name string, stdin io.Reader) ([]byte, bool) {
	src := stdin
	if name != "" && name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return nil, false
		}
		defer f.Close()
		src = f
	}
	data, err := io.ReadAll(io.LimitReader(src, record.MaxBytes+1))
	return data, err == nil
}

func write(dir string, res compile.Result) bool {
	spec, err := buildspec.Encode(res.BuildSpec)
	if err != nil {
		return false
	}
	// The item id was checked to be lower-case letters, digits and hyphens.
	base := filepath.Join(dir, res.ItemID)
	return os.WriteFile(base+".credit-note.txt", []byte(res.CreditNote), 0o644) == nil &&
		os.WriteFile(base+".build-spec.json", spec, 0o644) == nil
}
