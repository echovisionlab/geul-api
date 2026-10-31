package errors

import (
	"context"
	stderrors "errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// MsgInternal is the only text a client receives for a server fault.
const MsgInternal = "internal error"

// BoundaryInterceptor is the server edge for Connect handler failures. Place it
// inside the request log interceptor so request.completed only sees what the
// client sees. It
//   - recovers handler and inner-interceptor panics as CodeInternal and logs
//     them at Error with the stack;
//   - replaces Unknown, Internal and DataLoss errors with an opaque message and
//     logs their full cause at Error with the request context;
//   - logs operation context that classification kept from the client, at the
//     level of the classified code;
//   - removes private request values (tokens, passwords, codes, addresses)
//     from client messages and logged causes.
type BoundaryInterceptor struct{}

// NewBoundaryInterceptor returns the Connect handler failure boundary.
func NewBoundaryInterceptor() *BoundaryInterceptor { return &BoundaryInterceptor{} }

func (*BoundaryInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (resp connect.AnyResponse, err error) {
		if req.Spec().IsClient {
			return next(ctx, req)
		}
		procedure := req.Spec().Procedure
		defer func() {
			if recovered := recover(); recovered != nil {
				// net/http compares with ==; keep its abort semantics.
				if recovered == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, compared like net/http
					panic(recovered)
				}
				resp, err = nil, handlerPanic(ctx, procedure, recovered, requestPrivateValues(req.Any()))
			}
		}()
		resp, err = next(ctx, req)
		if err != nil {
			return resp, boundaryError(ctx, procedure, err, requestPrivateValues(req.Any()))
		}
		return resp, nil
	}
}

func (*BoundaryInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (*BoundaryInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) (err error) {
		procedure := conn.Spec().Procedure
		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, compared like net/http
					panic(recovered)
				}
				err = handlerPanic(ctx, procedure, recovered, nil)
			}
		}()
		if err = next(ctx, conn); err != nil {
			return boundaryError(ctx, procedure, err, nil)
		}
		return nil
	}
}

func handlerPanic(ctx context.Context, procedure string, recovered any, private []string) *connect.Error {
	cause, stack := failurecause.Panic(recovered, private...)
	slog.ErrorContext(ctx, "RPC handler panicked", "rpc_procedure", procedure, "failure_cause", cause, "panic_stack", stack)
	return connect.NewError(connect.CodeInternal, stderrors.New(MsgInternal))
}

func boundaryError(ctx context.Context, procedure string, err error, private []string) *connect.Error {
	classified := Wrap(err)
	code := classified.Code()
	switch code {
	case connect.CodeUnknown, connect.CodeInternal, connect.CodeDataLoss:
		slog.ErrorContext(ctx, "RPC failed", "rpc_procedure", procedure, "error_code", code.String(),
			"failure_cause", failurecause.Message(stderrors.New(operationCause(classified)), private...))
		return connect.NewError(code, stderrors.New(MsgInternal))
	}
	if _, kept := classified.Unwrap().(*classifiedCause); kept {
		slog.Log(ctx, OperationLogLevel(classified), "Classified RPC error", "rpc_procedure", procedure, "error_code", code.String(),
			"failure_cause", failurecause.Message(stderrors.New(operationCause(classified)), private...))
	}
	return redactClientMessage(classified, private)
}

// redactClientMessage removes private request values from a client message.
// The request log records the message, and a log is not the requester.
func redactClientMessage(err *connect.Error, private []string) *connect.Error {
	message := redactPrivateValues(err.Message(), private)
	if message == err.Message() {
		return err
	}
	redacted := connect.NewError(err.Code(), stderrors.New(message))
	for _, detail := range err.Details() {
		redacted.AddDetail(detail)
	}
	for key, values := range err.Meta() {
		redacted.Meta()[key] = append([]string(nil), values...)
	}
	return redacted
}

// minPrivateValueLength matches failurecause: shorter values would erase
// unrelated parts of a message.
const minPrivateValueLength = 4

func redactPrivateValues(message string, private []string) string {
	values := append([]string(nil), private...)
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		if len([]rune(value)) >= minPrivateValueLength {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	return message
}

// privateFieldWords mark request fields whose values must not reach logs.
var privateFieldWords = map[string]bool{
	"token": true, "password": true, "passcode": true, "secret": true, "otp": true,
	"email": true, "recipient": true, "identifier": true, "credential": true,
	"cookie": true, "signature": true,
}

// privateFieldNames are names, or name suffixes, whose words alone are too
// broad: "code" and "key" also name country_code or object_key.
var privateFieldNames = []string{
	"verification_code", "auth_code", "login_code", "recovery_code",
	"api_key", "secret_key", "private_key", "access_key",
}

func isPrivateField(name protoreflect.Name) bool {
	field := string(name)
	if field == "code" {
		return true
	}
	for _, word := range strings.Split(field, "_") {
		if privateFieldWords[word] {
			return true
		}
	}
	for _, private := range privateFieldNames {
		if field == private || strings.HasSuffix(field, "_"+private) {
			return true
		}
	}
	return false
}

// maxPrivateValueDepth bounds the walk through nested request messages.
const maxPrivateValueDepth = 8

// requestPrivateValues collects the string values of private request fields.
func requestPrivateValues(message any) []string {
	request, ok := message.(proto.Message)
	if !ok || request == nil {
		return nil
	}
	var values []string
	collectPrivateValues(request.ProtoReflect(), &values, 0)
	return values
}

func collectPrivateValues(message protoreflect.Message, values *[]string, depth int) {
	if depth > maxPrivateValueDepth || !message.IsValid() {
		return
	}
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		private := isPrivateField(field.Name())
		switch {
		case field.IsList():
			list := value.List()
			for index := range list.Len() {
				collectPrivateValue(field, list.Get(index), private, values, depth)
			}
		case field.IsMap():
			value.Map().Range(func(_ protoreflect.MapKey, entry protoreflect.Value) bool {
				collectPrivateValue(field.MapValue(), entry, private, values, depth)
				return true
			})
		default:
			collectPrivateValue(field, value, private, values, depth)
		}
		return true
	})
}

func collectPrivateValue(field protoreflect.FieldDescriptor, value protoreflect.Value, private bool, values *[]string, depth int) {
	switch field.Kind() {
	case protoreflect.StringKind:
		if private {
			*values = append(*values, value.String())
		}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		collectPrivateValues(value.Message(), values, depth+1)
	}
}
