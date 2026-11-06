package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"nacfson_pipeline/services/error-pipeline/internal/conform"
)

type violationOutput struct {
	Line    int    `json:"line"`
	Section string `json:"section"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, in io.Reader, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("logcheck", flag.ContinueOnError)
	fs.SetOutput(errOut)

	format := fs.String("format", "json", "log format to validate: json or nginx")
	expectVersion := fs.String("expect-version", "", "expected service.version string (e.g. v1.4.2)")
	expectService := fs.String("expect-service", "", "expected service.name string (e.g. pn-api)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *format != "json" && *format != "nginx" {
		fmt.Fprintf(errOut, "logcheck: invalid --format %q; must be 'json' or 'nginx'\n", *format)
		return 2
	}

	opts := conform.Options{
		Format:        *format,
		ExpectVersion: *expectVersion,
		ExpectService: *expectService,
	}

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNum := 0
	totalViolations := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		violations := conform.Check(line, opts)
		for _, v := range violations {
			totalViolations++
			vo := violationOutput{
				Line:    lineNum,
				Section: v.Section,
				Kind:    v.Kind,
				Text:    v.Text,
			}
			b, err := json.Marshal(vo)
			if err != nil {
				fmt.Fprintf(errOut, "logcheck: marshal error: %v\n", err)
				return 2
			}
			fmt.Fprintln(out, string(b))
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(errOut, "logcheck: scan error: %v\n", err)
		return 2
	}

	fmt.Fprintf(out, "logcheck: %d violation(s) found across %d line(s)\n", totalViolations, lineNum)

	if totalViolations > 0 {
		return 1
	}
	return 0
}
