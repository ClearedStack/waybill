// Command waybill compiles approved records standalone or runs the Muster
// contract v1 worker loop.
//
//	waybill compile [-out DIR] [FILE]
//	waybill run [-standin] [-seed FILE] [-once] [-spool DIR]
//	waybill version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ClearedStack/waybill/internal/buildspec"
	"github.com/ClearedStack/waybill/internal/compile"
	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/muster"
	"github.com/ClearedStack/waybill/internal/record"
	"github.com/ClearedStack/waybill/internal/spool"
	"github.com/ClearedStack/waybill/internal/strictjson"
	"github.com/ClearedStack/waybill/internal/worker"
)

// Version is the version in component.json.
const Version = "v0.1.0-draft"

// DefaultSpoolDir is the writable local directory mounted in deploy/waybill.yaml.
const DefaultSpoolDir = "/var/spool/cs"

// Exit codes.
const (
	exitOK       = 0
	exitFault    = 1 // one of the three public errors; see the printed result
	exitUsage    = 2
	exitInternal = 3
)

const usage = `usage:
  waybill compile [-out DIR] [FILE]                        compile one approved record (FILE or standard input)
  waybill run [-standin] [-seed FILE] [-once] [-spool DIR] run the Muster contract v1 worker
  waybill version                                          print the version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runContext(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runContext(context.Background(), args, stdin, stdout, stderr)
}

func runContext(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "version":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return exitUsage
		}
		fmt.Fprintln(stdout, "waybill", Version)
		return exitOK
	case "compile":
		return runCompile(args[1:], stdin, stdout, stderr)
	case "run":
		return runWorker(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
}

func runCompile(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("compile", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	outDir := flags.String("out", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() > 1 {
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
	_, _ = stdout.Write(out)
	if res.Error != nil {
		return exitFault
	}
	return exitOK
}

func runWorker(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	useStandin := flags.Bool("standin", false, "")
	seedPath := flags.String("seed", "", "")
	once := flags.Bool("once", false, "")
	spoolFlag := flags.String("spool", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	if *seedPath != "" && !*useStandin {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	contract := strings.TrimSpace(os.Getenv("CONTRACT_VERSION"))
	if contract == "" {
		contract = muster.ContractVersion
	}
	if contract != muster.ContractVersion {
		writeFaultJSON(stdout, fault.Unfamiliar("contract_version", "no rule covers this contract version"))
		return exitFault
	}

	spoolDir := strings.TrimSpace(*spoolFlag)
	if spoolDir == "" {
		spoolDir = strings.TrimSpace(os.Getenv("WAYBILL_SPOOL_DIR"))
	}
	if spoolDir == "" {
		spoolDir = DefaultSpoolDir
	}

	pollInterval, ok := parseEnvDuration("WAYBILL_POLL_INTERVAL", worker.DefaultPollInterval)
	if !ok {
		writeFaultJSON(stdout, fault.Broken("poll_interval", "WAYBILL_POLL_INTERVAL is malformed"))
		return exitFault
	}
	hbInterval, ok := parseEnvDuration("WAYBILL_HEARTBEAT_INTERVAL", worker.DefaultHeartbeatInterval)
	if !ok {
		writeFaultJSON(stdout, fault.Broken("heartbeat_interval", "WAYBILL_HEARTBEAT_INTERVAL is malformed"))
		return exitFault
	}
	httpTimeout, ok := parseEnvDuration("WAYBILL_HTTP_TIMEOUT", 10*time.Second)
	if !ok {
		writeFaultJSON(stdout, fault.Broken("http_timeout", "WAYBILL_HTTP_TIMEOUT is malformed"))
		return exitFault
	}

	baseURL := strings.TrimSpace(os.Getenv("MUSTER_URL"))
	tokenFile := strings.TrimSpace(os.Getenv("WAYBILL_TOKEN_FILE"))
	token := ""

	if *useStandin {
		standin := muster.NewStandIn()
		if *seedPath != "" {
			seedData, ok := read(*seedPath, strings.NewReader(""))
			if !ok {
				writeFaultJSON(stdout, fault.Broken("seed", "the stand-in seed file could not be read"))
				return exitFault
			}
			var items []muster.SeedItem
			if err := strictjson.Decode(seedData, &items); err != nil {
				writeFaultJSON(stdout, fault.Broken("seed", "the stand-in seed file is not valid JSON"))
				return exitFault
			}
			standin.Seed(items)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fmt.Fprintln(stderr, "waybill: could not start local stand-in listener")
			return exitInternal
		}
		srv := &http.Server{Handler: standin}
		go func() {
			_ = srv.Serve(ln)
		}()
		defer func() {
			_ = srv.Close()
		}()
		baseURL = "http://" + ln.Addr().String()
		tokenFile = ""
		token = "waybill"
	} else if tokenFile == "" {
		tokenFile = muster.DefaultTokenFile
	}

	client, err := muster.NewHTTPClient(muster.HTTPConfig{
		BaseURL:   baseURL,
		Contract:  contract,
		TokenFile: tokenFile,
		Token:     token,
		Timeout:   httpTimeout,
	})
	if err != nil {
		var fe *fault.Error
		if errors.As(err, &fe) {
			writeFaultJSON(stdout, fe)
		} else {
			writeFaultJSON(stdout, fault.Broken("muster_url", "the Muster client configuration is invalid"))
		}
		return exitFault
	}

	store, err := spool.Open(spoolDir)
	if err != nil {
		fmt.Fprintln(stderr, "waybill: could not open spool directory")
		return exitInternal
	}

	w, err := worker.New(worker.Config{
		Client:            client,
		Spool:             store,
		PollInterval:      pollInterval,
		HeartbeatInterval: hbInterval,
		Log:               stderr,
	})
	if err != nil {
		fmt.Fprintln(stderr, "waybill: could not initialize worker")
		return exitInternal
	}

	if *once {
		for {
			rep, err := w.Step(ctx)
			if err != nil {
				writeFaultJSON(stdout, &fault.Error{
					Code:    muster.CodeOf(err),
					Message: "worker step failed",
				})
				return exitFault
			}
			if !rep.Leased && rep.Drained == 0 {
				return exitOK
			}
		}
	}

	if err := w.Run(ctx); err != nil {
		return exitInternal
	}
	return exitOK
}

func parseEnvDuration(key string, def time.Duration) (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

func writeFaultJSON(w io.Writer, f *fault.Error) {
	out, err := buildspec.Encode(compile.Result{Error: f})
	if err == nil {
		_, _ = w.Write(out)
	}
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
	// ItemID was checked to be PA-0001..PA-999999999 or lower-case letters,
	// digits and hyphens.
	base := filepath.Join(dir, res.ItemID)
	return os.WriteFile(base+".credit-note.txt", []byte(res.CreditNote), 0o644) == nil &&
		os.WriteFile(base+".build-spec.json", spec, 0o644) == nil
}
