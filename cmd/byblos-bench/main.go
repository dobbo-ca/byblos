// Command byblos-bench measures what each shipped byblos capability costs and
// scores one measurement against another.
//
// It is test tooling, not part of the library. See
// docs/superpowers/specs/2026-08-11-bench-map-design.md.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/dobbo-ca/byblos/internal/bench"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "measure":
		err = cmdMeasure(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "score":
		err = cmdScore(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "byblos-bench: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  byblos-bench measure -capability C -doc PATH [-reps N]
  byblos-bench run -set DIR -out FILE [-time C1,C2] [-reps N]
  byblos-bench score -baseline FILE -head FILE [-out FILE]
`)
	os.Exit(2)
}

// cmdMeasure is the child process: one capability, one document, JSON on
// stdout. Forked by cmdRun so each measurement gets its own process-wide
// counters.
func cmdMeasure(args []string) error {
	fs := flag.NewFlagSet("measure", flag.ExitOnError)
	capability := fs.String("capability", "", "capability string")
	doc := fs.String("doc", "", "path to a PDF")
	reps := fs.Int("reps", 0, "timing repetitions; 0 means do not time")
	if err := fs.Parse(args); err != nil {
		return err
	}
	body, err := os.ReadFile(*doc)
	if err != nil {
		return err
	}
	s, err := measure(*capability, filepathBase(*doc), body, *reps)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(s)
}

// filepathBase is a one-line wrapper over path/filepath.Base.
func filepathBase(path string) string { return filepath.Base(path) }

// cmdRun forks one measure child per (capability, document) pair and collects
// the samples. An ineligible pair is skipped, not fatal.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	set := fs.String("set", "", "directory of the bench set")
	out := fs.String("out", "", "where to write the run JSON")
	timed := fs.String("time", "", "comma-separated capabilities to time")
	reps := fs.Int("reps", 5, "timing repetitions for timed capabilities")
	if err := fs.Parse(args); err != nil {
		return err
	}

	docs, err := filepath.Glob(filepath.Join(*set, "*.pdf"))
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return fmt.Errorf("no PDFs in %s", *set)
	}

	timeThese := make(map[string]bool)
	for _, c := range strings.Split(*timed, ",") {
		if c = strings.TrimSpace(c); c != "" {
			timeThese[c] = true
		}
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}

	run := bench.Run{
		GoVersion:  runtime.Version(),
		GOOSGOARCH: runtime.GOOS + "/" + runtime.GOARCH,
	}
	for _, tg := range bench.Targets {
		n := 0
		if timeThese[tg.Capability] {
			n = *reps
		}
		for _, doc := range docs {
			s, err := fork(self, tg.Capability, doc, n)
			if errors.Is(err, errChildSkipped) {
				continue
			}
			if err != nil {
				return fmt.Errorf("%s over %s: %w", tg.Capability, filepath.Base(doc), err)
			}
			run.Samples = append(run.Samples, s)
		}
	}

	body, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, append(body, '\n'), 0o644)
}

var errChildSkipped = errors.New("child reported the document ineligible")

// fork runs one measure child and decodes its sample. A child that exits
// non-zero having printed ErrIneligible is a skip; any other non-zero exit is
// a failure of the run.
func fork(self, capability, doc string, reps int) (bench.Sample, error) {
	cmd := exec.Command(self, "measure",
		"-capability", capability, "-doc", doc, "-reps", strconv.Itoa(reps))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), bench.ErrIneligible.Error()) {
			return bench.Sample{}, errChildSkipped
		}
		return bench.Sample{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var s bench.Sample
	if err := json.Unmarshal(stdout.Bytes(), &s); err != nil {
		return bench.Sample{}, fmt.Errorf("decode child output: %w", err)
	}
	return s, nil
}

// cmdScore compares a head run against a committed baseline and prints the
// markdown table. It exits non-zero when the candidate fails, so the workflow
// can branch without parsing the table.
func cmdScore(args []string) error {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	baselinePath := fs.String("baseline", "internal/bench/baseline.json", "committed baseline")
	headPath := fs.String("head", "", "head run JSON")
	out := fs.String("out", "", "where to write the markdown table; stdout if empty")
	if err := fs.Parse(args); err != nil {
		return err
	}

	base, err := bench.LoadBaseline(*baselinePath)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(*headPath)
	if err != nil {
		return err
	}
	var head bench.Run
	if err := json.Unmarshal(body, &head); err != nil {
		return err
	}

	res := bench.Score(base, head)
	md := res.Markdown()
	if *out != "" {
		if err := os.WriteFile(*out, []byte(md), 0o644); err != nil {
			return err
		}
	} else {
		fmt.Print(md)
	}
	if !res.Pass {
		os.Exit(1)
	}
	return nil
}
