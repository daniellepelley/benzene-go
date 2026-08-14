// Command benzene-codegen generates a typed, topic-scoped Go client SDK from a Benzene service's
// committed Contract Document (docs/specification/contract-document.md in
// daniellepelley/Benzene) - the Go port of the .NET repo's `benzene build` CLI
// (Benzene.CodeGen.Client), see docs/codegen-client.md in this repo for the full guide.
//
// Usage:
//
//	benzene-codegen build -file contracts/payments.spec.json -out ./payments -package payments -service Payments
//	benzene-codegen build -file contracts/payments.spec.json -out ./paymentscapture -mode topic -topics payments:capture
//
// Exits non-zero (with a message on stderr) on an unknown flag, an unparseable document, or an
// unknown topic named in -topics.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/daniellepelley/benzene-go/codegen/contractdoc"
	"github.com/daniellepelley/benzene-go/codegen/gengo"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "benzene-codegen: expected a subcommand (\"build\")")
		return 2
	}

	switch args[0] {
	case "build":
		return runBuild(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "benzene-codegen: unknown subcommand %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: benzene-codegen build -file <contract.spec.json> -out <dir> [flags]")
	fmt.Fprintln(w, "  -mode service|topic     generated shape (default service)")
	fmt.Fprintln(w, "  -service <name>         service name (mode=service; required there)")
	fmt.Fprintln(w, "  -package <name>         Go package name (mode=service; required there)")
	fmt.Fprintln(w, "  -topics <a,b,c>         topic include-list (comma-separated)")
	fmt.Fprintln(w, "  -include-reserved       admit reserved benzene:* topics with no include-list")
}

func runBuild(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)

	file := fs.String("file", "", "path to the Contract Document (*.spec.json)")
	out := fs.String("out", "", "output directory")
	mode := fs.String("mode", "service", "\"service\" (one client covering every in-scope topic) or \"topic\" (one self-contained client per topic, contract-document.md §5.3)")
	serviceName := fs.String("service", "", "service name (mode=service)")
	packageName := fs.String("package", "", "Go package name (mode=service)")
	topicsFlag := fs.String("topics", "", "comma-separated topic include-list (contract-document.md §5.2)")
	includeReserved := fs.Bool("include-reserved", false, "admit reserved benzene:* topics when -topics is not given")

	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError already printed the error/usage to stderr.
		return 2
	}

	if *file == "" {
		fmt.Fprintln(stderr, "benzene-codegen build: -file is required")
		return 2
	}
	if *out == "" {
		fmt.Fprintln(stderr, "benzene-codegen build: -out is required")
		return 2
	}

	raw, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(stderr, "benzene-codegen build: reading %s: %v\n", *file, err)
		return 1
	}

	doc, err := contractdoc.Parse(raw)
	if err != nil {
		fmt.Fprintf(stderr, "benzene-codegen build: parsing %s: %v\n", *file, err)
		return 1
	}

	var topics []string
	if strings.TrimSpace(*topicsFlag) != "" {
		for _, t := range strings.Split(*topicsFlag, ",") {
			if t = strings.TrimSpace(t); t != "" {
				topics = append(topics, t)
			}
		}
	}

	switch *mode {
	case "service":
		if *serviceName == "" {
			fmt.Fprintln(stderr, "benzene-codegen build: -service is required for mode=service")
			return 2
		}
		if *packageName == "" {
			fmt.Fprintln(stderr, "benzene-codegen build: -package is required for mode=service")
			return 2
		}

		scoped, err := contractdoc.ApplyScope(doc, contractdoc.ScopeOptions{Topics: topics, IncludeReserved: *includeReserved})
		if err != nil {
			fmt.Fprintf(stderr, "benzene-codegen build: %v\n", err)
			return 1
		}

		files, err := gengo.GenerateServiceClient(scoped, gengo.ServiceOptions{ServiceName: *serviceName, PackageName: *packageName})
		if err != nil {
			fmt.Fprintf(stderr, "benzene-codegen build: %v\n", err)
			return 1
		}
		if err := writeFiles(*out, files); err != nil {
			fmt.Fprintf(stderr, "benzene-codegen build: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %d file(s) to %s\n", len(files), *out)
		return 0

	case "topic":
		clients, err := gengo.GenerateAtomicClients(doc, gengo.AtomicOptions{Topics: topics, IncludeReserved: *includeReserved})
		if err != nil {
			fmt.Fprintf(stderr, "benzene-codegen build: %v\n", err)
			return 1
		}
		total := 0
		for _, c := range clients {
			dir := filepath.Join(*out, c.Dir)
			if err := writeFiles(dir, c.Files); err != nil {
				fmt.Fprintf(stderr, "benzene-codegen build: %v\n", err)
				return 1
			}
			total += len(c.Files)
		}
		fmt.Fprintf(stdout, "wrote %d client(s), %d file(s) total, under %s\n", len(clients), total, *out)
		return 0

	default:
		fmt.Fprintf(stderr, "benzene-codegen build: unknown -mode %q (want \"service\" or \"topic\")\n", *mode)
		return 2
	}
}

func writeFiles(dir string, files []gengo.GeneratedFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	for _, f := range files {
		path := filepath.Join(dir, f.Name)
		if err := os.WriteFile(path, []byte(f.Source), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return nil
}
