package main

// Greeter is the demo service the greet handler depends on - a "port" in hexagonal terms. The
// handler knows only this interface, never how a greeting is actually produced, so swapping the
// adapter below for one that reads a template from configuration, calls another service, or
// localises the message needs no change to the handler or its registration. This is the one
// injected dependency the starter ships with; register your own services the same way in
// newApp's ConfigureServices.
type Greeter interface {
	Greet(name string) string
}

// helloGreeter is the adapter wired in by default - process-local and trivial, which is all a
// starter needs.
type helloGreeter struct{}

func (helloGreeter) Greet(name string) string {
	return "Hello, " + name + "!"
}

// greeterKey is the DI container key the greet handler resolves its Greeter by.
const greeterKey = "greeter"
