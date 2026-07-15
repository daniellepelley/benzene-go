package benzene

import (
	"context"
	"testing"
)

func TestPipeline_EmptyPipelineRunsWithoutError(t *testing.T) {
	p := NewPipeline()
	ic := NewInvocationContext(NewTopic("t"), nil, nil, nil)
	if err := p.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
}

func TestPipeline_RunsMiddlewareInRegistrationOrder(t *testing.T) {
	var order []string
	mw := func(name string) Middleware {
		return func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) error {
			order = append(order, name+":before")
			err := next(ctx)
			order = append(order, name+":after")
			return err
		}
	}

	p := NewPipeline(mw("first"), mw("second"))
	ic := NewInvocationContext(NewTopic("t"), nil, nil, nil)
	if err := p.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{"first:before", "second:before", "second:after", "first:after"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q (full: %v)", i, order[i], want[i], order)
		}
	}
}

func TestPipeline_ShortCircuit_NotCallingNextStopsTheChain(t *testing.T) {
	ran := map[string]bool{}

	shortCircuiting := func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) error {
		ran["short-circuiting"] = true
		// deliberately does not call next
		return nil
	}
	shouldNotRun := func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) error {
		ran["should-not-run"] = true
		return next(ctx)
	}

	p := NewPipeline(shortCircuiting, shouldNotRun)
	ic := NewInvocationContext(NewTopic("t"), nil, nil, nil)
	if err := p.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !ran["short-circuiting"] {
		t.Error("the short-circuiting middleware should have run")
	}
	if ran["should-not-run"] {
		t.Error("middleware after a short-circuit should not run")
	}
}

func TestPipeline_ErrorPropagatesUp(t *testing.T) {
	wantErr := context.Canceled
	failing := func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) error {
		return wantErr
	}

	p := NewPipeline(failing)
	ic := NewInvocationContext(NewTopic("t"), nil, nil, nil)
	if err := p.Run(context.Background(), ic); err != wantErr {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
}

func TestPipeline_MiddlewareCanMutateInvocationContext(t *testing.T) {
	setsHeader := func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) error {
		ic.Headers["injected"] = "yes"
		return next(ctx)
	}
	var seenHeader string
	reads := func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) error {
		seenHeader = ic.Headers["injected"]
		return next(ctx)
	}

	p := NewPipeline(setsHeader, reads)
	ic := NewInvocationContext(NewTopic("t"), nil, nil, nil)
	if err := p.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if seenHeader != "yes" {
		t.Errorf("seenHeader = %q, want %q", seenHeader, "yes")
	}
}
