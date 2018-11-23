package typhon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/monzo/terrors"
)

// A Request is Typhon's wrapper around http.Request, used by both clients and servers.
type Request struct {
	http.Request
	context.Context
	Error error // Any error from request construction
}

// unwrappedContext returns the most "unwrapped" Context possible for that in the request.
// This is useful as it's very often the case that Typhon users will use a parent request
// as a parent for a child request. The context library knows how to unwrap its own
// types to most efficiently perform certain operations (eg. cancellation chaining), but
// it can't do that with Typhon-wrapped contexts.
func (r *Request) unwrappedContext() context.Context {
	switch c := r.Context.(type) {
	case Request:
		return c.unwrappedContext()
	case *Request:
		return c.unwrappedContext()
	default:
		return c
	}
}

// Encode serialises the passed object as JSON into the body (and sets appropriate headers).
func (r *Request) Encode(v interface{}) {
	cw := &countingWriter{
		Writer: r}
	if err := json.NewEncoder(cw).Encode(v); err != nil {
		r.Error = terrors.Wrap(err, nil)
		return
	}
	r.Header.Set("Content-Type", "application/json")
	if r.ContentLength < 0 && cw.n < chunkThreshold {
		r.ContentLength = int64(cw.n)
	}
}

// Decode de-serialises the JSON body into the passed object.
func (r Request) Decode(v interface{}) error {
	b, err := r.BodyBytes(true)
	if err == nil {
		err = json.Unmarshal(b, v)
	}
	return terrors.WrapWithCode(err, nil, terrors.ErrBadRequest)
}

func (r *Request) Write(b []byte) (int, error) {
	switch rc := r.Body.(type) {
	// In the "normal" case, the response body will be a buffer, to which we can write
	case io.Writer:
		return rc.Write(b)
	// If a caller manually sets Response.Body, then we may not be able to write to it. In that case, we need to be
	// cleverer.
	default:
		buf := &bufCloser{}
		if _, err := io.Copy(buf, rc); err != nil {
			// This can be quite bad; we have consumed (and possibly lost) some of the original body
			return 0, err
		}
		// rc will never again be accessible: once it's copied it must be closed
		rc.Close()
		r.Body = buf
		return buf.Write(b)
	}
}

// BodyBytes fully reads the request body and returns the bytes read. If consume is false, the body is copied into a
// new buffer such that it may be read again.
func (r *Request) BodyBytes(consume bool) ([]byte, error) {
	if consume {
		defer r.Body.Close()
		return ioutil.ReadAll(r.Body)
	}

	switch rc := r.Body.(type) {
	case *bufCloser:
		return rc.Bytes(), nil
	default:
		buf := &bufCloser{}
		r.Body = buf
		rdr := io.TeeReader(rc, buf)
		// rc will never again be accessible: once it's copied it must be closed
		defer rc.Close()
		return ioutil.ReadAll(rdr)
	}
}

func (r Request) Send() *ResponseFuture {
	return Send(r)
}

func (r Request) SendVia(svc Service) *ResponseFuture {
	return SendVia(r, svc)
}

// Response construct a new Response to the request, and if non-nil, encodes the given body into it.
func (r Request) Response(body interface{}) Response {
	rsp := NewResponse(r)
	if body != nil {
		rsp.Encode(body)
	}
	return rsp
}

func (r Request) String() string {
	if r.URL == nil {
		return "Request(Unknown)"
	}
	return fmt.Sprintf("Request(%s %s://%s%s)", r.Method, r.URL.Scheme, r.Host, r.URL.Path)
}

// NewRequest constructs a new Request with the given parameters, and if non-nil, encodes the given body into it.
func NewRequest(ctx context.Context, method, url string, body interface{}) Request {
	if ctx == nil {
		ctx = context.Background()
	}
	httpReq, err := http.NewRequest(method, url, nil)
	req := Request{
		Context: ctx,
		Error:   err}
	if httpReq != nil {
		httpReq.ContentLength = -1
		httpReq.Body = &bufCloser{}
		req.Request = *httpReq
	}
	if body != nil && err == nil {
		req.Encode(body)
	}
	return req
}
