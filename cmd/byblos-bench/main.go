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
	case "baseline":
		err = cmdBaseline(os.Args[2:])
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
  byblos-bench baseline -runs A.json,B.json,C.json -out FILE -commit SHA -benchset SHA
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
			if err != nil {
				run.Skipped = append(run.Skipped, bench.Skip{
					Capability: tg.Capability,
					Doc:        filepath.Base(doc),
					Reason:     skipReason(err),
				})
				continue
			}
			run.Samples = append(run.Samples, s)
		}
	}

	// A run with no samples is a broken harness reporting success. Refuse it
	// rather than writing a file every downstream step would treat as measured.
	if len(run.Samples) == 0 {
		return fmt.Errorf("measured nothing: %d skips over %d documents", len(run.Skipped), len(docs))
	}
	fmt.Fprintf(os.Stderr, "byblos-bench: %d samples, %d skipped\n", len(run.Samples), len(run.Skipped))

	body, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, append(body, '\n'), 0o644)
}

var errChildSkipped = errors.New("child reported the document ineligible")

// skipReason turns a fork failure into the text recorded on the skip.
//
// The two cases are kept distinct because they mean different things to a
// reader of the run: "ineligible" is the capability declining a document it
// does not apply to, while anything else is a document byblos could not read.
func skipReason(err error) string {
	if errors.Is(err, errChildSkipped) {
		return "ineligible"
	}
	return err.Error()
}

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

// cmdBaseline reduces N repeated runs of the SAME commit to a committed
// baseline.
//
// N must be at least 3. One run cannot show how much a number moves on its own,
// and design spec section 2.1 measured that several of them move: without the
// spreads these repetitions produce, a candidate that changed nothing can pass
// on jitter.
func cmdBaseline(args []string) error {
	fs := flag.NewFlagSet("baseline", flag.ExitOnError)
	runs := fs.String("runs", "", "comma-separated run JSON files, same commit")
	out := fs.String("out", "internal/bench/baseline.json", "where to write the baseline")
	commit := fs.String("commit", "", "the commit these runs measured")
	benchSet := fs.String("benchset", "", "sha256 of the bench set archive")
	harness := fs.String("harness", "", "sha256 over cmd/byblos-bench and internal/bench/map.go")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var paths []string
	for _, p := range strings.Split(*runs, ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) < 3 {
		return fmt.Errorf("got %d runs, need at least 3 to measure a spread", len(paths))
	}

	var loaded []bench.Run
	for _, p := range paths {
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var r bench.Run
		if err := json.Unmarshal(body, &r); err != nil {
			return fmt.Errorf("decode %s: %w", p, err)
		}
		loaded = append(loaded, r)
	}

	for i, r := range loaded[1:] {
		if r.GoVersion != loaded[0].GoVersion || r.GOOSGOARCH != loaded[0].GOOSGOARCH {
			return fmt.Errorf("run %s was measured on a different toolchain or platform than %s",
				paths[i+1], paths[0])
		}
	}

	b := bench.BaselineFromRuns(loaded, bench.Fingerprint{
		BenchSetSHA256: *benchSet,
		HarnessSHA256:  *harness,
		Commit:         *commit,
		GoVersion:      loaded[0].GoVersion,
		GOOSGOARCH:     loaded[0].GOOSGOARCH,
	})

	body, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, append(body, '\n'), 0o644)
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
