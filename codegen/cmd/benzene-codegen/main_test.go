package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDoc = `{
  "openapi": "3.0.1",
  "info": {"title": "orders-api", "version": "1.0.0"},
  "requests": [
    {"topic": "orders:create", "request": {"$ref": "#/components/schemas/CreateOrder"}, "response": {"$ref": "#/components/schemas/OrderDto"}},
    {"topic": "benzene:spec", "reserved": true, "request": {"$ref": "#/components/schemas/Void"}, "response": {"$ref": "#/components/schemas/Void"}}
  ],
  "events": [],
  "components": {"schemas": {
    "CreateOrder": {"type": "object", "properties": {"customerId": {"type": "string"}}, "required": ["customerId"]},
    "OrderDto": {"type": "object", "properties": {"id": {"type": "string"}}},
    "Void": {"type": "object", "additionalProperties": false}
  }}
}`

func writeTestDoc(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "orders.spec.json")
	if err := os.WriteFile(path, []byte(testDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunBuild_ServiceMode(t *testing.T) {
	docPath := writeTestDoc(t)
	outDir := filepath.Join(t.TempDir(), "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "-file", docPath, "-out", outDir, "-service", "Orders", "-package", "orders"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}

	clientSrc, err := os.ReadFile(filepath.Join(outDir, "client.go"))
	if err != nil {
		t.Fatalf("reading client.go: %v", err)
	}
	if !strings.Contains(string(clientSrc), "func NewOrdersClient") {
		t.Errorf("client.go missing constructor:\n%s", clientSrc)
	}
	if strings.Contains(string(clientSrc), `"benzene:spec"`) {
		t.Error("domain-only default must not reference benzene:spec in client.go's methods")
	}

	if _, err := os.Stat(filepath.Join(outDir, "types.go")); err != nil {
		t.Errorf("types.go not written: %v", err)
	}
}

func TestRunBuild_TopicMode(t *testing.T) {
	docPath := writeTestDoc(t)
	outDir := filepath.Join(t.TempDir(), "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "-file", docPath, "-out", outDir, "-mode", "topic", "-topics", "orders:create"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}

	clientPath := filepath.Join(outDir, "orderscreate", "client.go")
	if _, err := os.Stat(clientPath); err != nil {
		t.Errorf("expected %s to exist: %v", clientPath, err)
	}
}

func TestRunBuild_UnknownTopicFailsLoud(t *testing.T) {
	docPath := writeTestDoc(t)
	outDir := filepath.Join(t.TempDir(), "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "-file", docPath, "-out", outDir, "-mode", "topic", "-topics", "not-a-topic"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected a non-zero exit code for an unknown topic")
	}
	if !strings.Contains(stderr.String(), "not-a-topic") {
		t.Errorf("stderr should name the unknown topic, got %q", stderr.String())
	}
}

func TestRunBuild_UnparseableDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.spec.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "-file", path, "-out", filepath.Join(dir, "out"), "-service", "X", "-package", "x"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected a non-zero exit code for an unparseable document")
	}
}

func TestRunBuild_MissingFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "-file", "/does/not/exist.json", "-out", t.TempDir(), "-service", "X", "-package", "x"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected a non-zero exit code for a missing file")
	}
}

func TestRun_UnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "-not-a-real-flag"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected a non-zero exit code for an unknown flag")
	}
}

func TestRun_MissingRequiredFlags(t *testing.T) {
	cases := [][]string{
		{"build"},
		{"build", "-file", "x.json"},
		{"build", "-file", "x.json", "-out", "o"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Errorf("args %v: expected a non-zero exit code", args)
		}
	}
}

func TestRun_NoSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code == 0 {
		t.Error("expected a non-zero exit code with no subcommand")
	}
}

func TestRun_UnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"bogus"}, &stdout, &stderr); code == 0 {
		t.Error("expected a non-zero exit code for an unknown subcommand")
	}
}

func TestRun_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-help"}, &stdout, &stderr); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout.Len() == 0 {
		t.Error("expected usage text on stdout")
	}
}

func TestRunBuild_UnknownMode(t *testing.T) {
	docPath := writeTestDoc(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "-file", docPath, "-out", t.TempDir(), "-mode", "bogus"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected a non-zero exit code for an unknown mode")
	}
}
