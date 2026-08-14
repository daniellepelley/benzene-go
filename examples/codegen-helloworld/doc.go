// Command codegen-helloworld (see main.go) dogfoods the client generator in the sibling `codegen`
// Go module (`codegen/cmd/benzene-codegen`) against a committed Contract Document
// (contracts/payments.spec.json - vendored verbatim from the .NET reference's
// Benzene.Descriptor-emitted example, examples/AwsMesh/Orders/contracts/payments.spec.json in
// daniellepelley/benzene-dotnet) - see docs/codegen-client.md for the full generator guide.
//
// paymentscapture/ is a generated, self-contained topic-scoped client (contract-document.md §5.3)
// for the payments:capture topic only - the //go:generate directive below regenerates it. Its
// files are committed, per Go's own go:generate convention (the tool is not run at build time);
// main_test.go proves the generated code actually works end to end against a fake client.Sender,
// and CI's `go generate ./... && git diff --exit-code` (docs/codegen-client.md's Phase 6 build
// check) catches drift between contracts/payments.spec.json and the committed generated files.
package main

// generate.sh does the actual work (see its own comment for why a plain `go run
// ../../codegen/cmd/benzene-codegen` one-liner does not work here).
//go:generate sh generate.sh
