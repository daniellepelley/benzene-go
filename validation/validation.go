// Package validation is the request-validation building block: a typed wrapper that runs a
// validator before a handler and short-circuits with a validation-error result when the request is
// invalid, so the handler only ever sees a valid request.
//
// It is the Go-idiomatic form of the .NET port's Benzene.DataAnnotations / Benzene.FluentValidation
// ValidationMiddleware. Those are pipeline middleware because .NET's message-handler pipeline is
// typed (it carries IMessageHandlerContext<TRequest, TResponse>). This port's pipeline is
// type-erased until the router dispatches - middleware sees the raw request bytes, not the typed
// TReq - so validation composes at the typed handler instead, as a plain wrapper at registration
// time (the same shape as wrapping an http.Handler). No reflection, no struct-tag DSL, no
// dependency: a Go service writes an ordinary Validate function and Validated turns it into the
// short-circuit the .NET middleware performs.
//
//	benzene.Register(registry, benzene.NewTopic("order:create"),
//	    validation.Validated(orderValidator, createOrderHandler))
//
// On failure the result is a benzene.ValidationError carrying the problems (wire-contracts.md §3's
// "validation-error" status), exactly as Benzene.DataAnnotations.ValidationMiddleware returns
// BenzeneResult.ValidationError - so every transport binding maps it to that transport's native
// validation-failure code without the handler running.
//
// A validator reports benzene.Error values, so it can say WHICH field failed and WHICH rule
// rejected it, and both reach the caller's problem document (wire-contracts.md §1.3) rather than
// being flattened into prose the caller has to parse. A validator with nothing to add beyond the
// message wraps a plain messages function with Messages:
//
//	validation.Validated(validation.Messages(orderMessages), createOrderHandler)
package validation

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
)

// Validator reports zero or more validation errors for a request of type T. An empty (or nil)
// result means the request is valid.
//
// The errors are structured (benzene.Error): Message is the only required member, and a validator
// that knows the Field the value came from and the Code of the rule that rejected it should say so -
// both travel to the caller intact. Messages adapts a validator that only has messages.
type Validator[T any] interface {
	Validate(request T) []benzene.Error
}

// ValidatorFunc adapts a plain function to a Validator, so a service can pass a function literal
// wherever a Validator is expected.
type ValidatorFunc[T any] func(request T) []benzene.Error

// Validate calls f.
func (f ValidatorFunc[T]) Validate(request T) []benzene.Error { return f(request) }

// Messages adapts a validator that reports plain message strings, wrapping each as a Message-only
// benzene.Error. It is the short path for a validator with nothing to add beyond the message, and
// keeps that case a single call rather than making every validator build structs:
//
//	validation.Messages(func(o createOrder) []string { ... })
func Messages[T any](validate func(request T) []string) Validator[T] {
	if validate == nil {
		return nil
	}
	return ValidatorFunc[T](func(request T) []benzene.Error {
		messages := validate(request)
		if len(messages) == 0 {
			return nil
		}
		errors := make([]benzene.Error, 0, len(messages))
		for _, message := range messages {
			errors = append(errors, benzene.Error{Message: message})
		}
		return errors
	})
}

// Validated wraps handler so request is validated before it runs. When validator returns any
// messages, handler is NOT called and a benzene.ValidationError result carrying those messages is
// returned (the short-circuit); otherwise the wrapped handler runs unchanged and its result is
// returned as-is. The wrapped value is an ordinary benzene.Handler[TReq, TRes], so it registers and
// composes exactly like any other handler. A nil validator wraps nothing - handler is returned
// as-is (the same nil-tolerance as Combine), rather than panicking at dispatch.
func Validated[TReq, TRes any](validator Validator[TReq], handler benzene.Handler[TReq, TRes]) benzene.Handler[TReq, TRes] {
	if validator == nil {
		return handler
	}
	return func(ctx context.Context, request TReq) benzene.Result[TRes] {
		if errors := validator.Validate(request); len(errors) > 0 {
			return benzene.ValidationErrorWith[TRes](errors...)
		}
		return handler(ctx, request)
	}
}

// Combine returns a Validator that runs each of validators in order and concatenates their
// messages, so several focused validators compose into one without the caller writing the plumbing.
// A nil entry is skipped. With no validators (or all nil) the combined validator always passes.
func Combine[T any](validators ...Validator[T]) Validator[T] {
	return ValidatorFunc[T](func(request T) []benzene.Error {
		var errors []benzene.Error
		for _, validator := range validators {
			if validator == nil {
				continue
			}
			errors = append(errors, validator.Validate(request)...)
		}
		return errors
	})
}
