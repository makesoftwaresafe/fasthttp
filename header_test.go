package fasthttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type slogTestHandler struct {
	out string
}

func (s *slogTestHandler) Enabled(_ context.Context, level slog.Level) bool {
	return true
}

func (s *slogTestHandler) Handle(ctx context.Context, record slog.Record) error { //nolint:gocritic
	s.out += record.Message
	for r := range record.Attrs {
		s.out += " " + r.Key + ":" + r.Value.String()
	}
	return nil
}

func (s *slogTestHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	for _, attr := range attrs {
		s.out += attr.String()
	}
	return &slogTestHandler{out: s.out}
}

func (s *slogTestHandler) WithGroup(name string) slog.Handler {
	return &slogTestHandler{out: s.out}
}

func TestNewlineBackwardsCompatibleWarning(t *testing.T) {
	h := &ResponseHeader{}

	l := slog.Default()
	ll := &slogTestHandler{}
	slog.SetDefault(slog.New(ll))
	defer slog.SetDefault(l)

	DeprecatedNewlineIncludeContext.Store(true)
	warnedAboutDeprecatedNewlineSeparatorLimiter.Store(0)

	if err := h.Read(bufio.NewReader(bytes.NewBufferString("HTTP/1.1 200 OK\r\nContent-Type: foo/bar\nContent-Length: 12345\r\n\r\nsss"))); err != nil {
		t.Fatal(err)
	}
	e := h.Peek(HeaderContentType)
	if string(e) != "foo/bar" {
		t.Fatalf("Unexpected Content-Type %q. Expected %q", e, "foo/bar")
	}
	expected := "Deprecated newline only separator found in header context:\"Content-Type: foo/bar\\nContent-Length: 123\""
	if ll.out != expected {
		t.Errorf("Expected %q, got %q", expected, ll.out)
	}

	ll.out = ""

	DeprecatedNewlineIncludeContext.Store(false)
	warnedAboutDeprecatedNewlineSeparatorLimiter.Store(0)

	if err := h.Read(bufio.NewReader(bytes.NewBufferString("HTTP/1.1 200 OK\r\nContent-Type: foo/bar\nContent-Length: 12345\r\n\r\nsss"))); err != nil {
		t.Fatal(err)
	}
	expected = "Deprecated newline only separator found in header"
	if ll.out != expected {
		t.Errorf("Expected %q, got %q", expected, ll.out)
	}
}

func TestResponseHeaderAddContentType(t *testing.T) {
	t.Parallel()

	var h ResponseHeader
	h.Add("Content-Type", "test")

	got := string(h.Peek("Content-Type"))
	expected := "test"
	if got != expected {
		t.Errorf("expected %q got %q", expected, got)
	}

	var buf bytes.Buffer
	if _, err := h.WriteTo(&buf); err != nil {
		t.Fatalf("unexpected error when writing header: %v", err)
	}

	if n := strings.Count(buf.String(), "Content-Type: "); n != 1 {
		t.Errorf("Content-Type occurred %d times", n)
	}
}

func TestResponseHeaderAddContentEncoding(t *testing.T) {
	t.Parallel()

	var h ResponseHeader
	h.Add("Content-Encoding", "test")

	got := string(h.Peek("Content-Encoding"))
	expected := "test"
	if got != expected {
		t.Errorf("expected %q got %q", expected, got)
	}

	var buf bytes.Buffer
	if _, err := h.WriteTo(&buf); err != nil {
		t.Fatalf("unexpected error when writing header: %v", err)
	}

	if n := strings.Count(buf.String(), "Content-Encoding: "); n != 1 {
		t.Errorf("Content-Encoding occurred %d times", n)
	}
}

func TestResponseHeaderMultiLineValue(t *testing.T) {
	t.Parallel()

	s := "HTTP/1.1 200 SuperOK\r\n" +
		"EmptyValue1:\r\n" +
		"Content-Type: foo/bar;\r\n\tnewline;\r\n another/newline\r\n" +
		"Foo: Bar\r\n" +
		"Multi-Line: one;\r\n two\r\n" +
		"Values: v1;\r\n v2; v3;\r\n v4;\tv5\r\n" +
		"\r\n"
	header := new(ResponseHeader)
	if _, err := header.parse([]byte(s)); err != nil {
		t.Fatalf("parse headers with multi-line values failed, %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(strings.NewReader(s)), nil)
	if err != nil {
		t.Fatalf("parse response using net/http failed, %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if !bytes.Equal(header.StatusMessage(), []byte("SuperOK")) {
		t.Errorf("parse status line with non-default value failed, got: '%q' want: 'SuperOK'", header.StatusMessage())
	}

	header.SetProtocol([]byte("HTTP/3.3"))
	if !bytes.Equal(header.Protocol(), []byte("HTTP/3.3")) {
		t.Errorf("parse protocol with non-default value failed, got: '%q' want: 'HTTP/3.3'", header.Protocol())
	}

	if !bytes.Equal(header.appendStatusLine(nil), []byte("HTTP/3.3 200 SuperOK\r\n")) {
		t.Errorf("parse status line with non-default value failed, got: '%q' want: 'HTTP/3.3 200 SuperOK'", header.Protocol())
	}

	header.SetStatusMessage(nil)

	if !bytes.Equal(header.appendStatusLine(nil), []byte("HTTP/3.3 200 OK\r\n")) {
		t.Errorf("parse status line with default protocol value failed, got: '%q' want: 'HTTP/3.3 200 OK'", header.appendStatusLine(nil))
	}

	header.SetStatusMessage(s2b(StatusMessage(200)))

	if !bytes.Equal(header.appendStatusLine(nil), []byte("HTTP/3.3 200 OK\r\n")) {
		t.Errorf("parse status line with default protocol value failed, got: '%q' want: 'HTTP/3.3 200 OK'", header.appendStatusLine(nil))
	}

	for name, vals := range response.Header {
		got := string(header.Peek(name))
		want := vals[0]

		if got != want {
			t.Errorf("unexpected %q got: %q want: %q", name, got, want)
		}
	}
}

func TestIssue1808(t *testing.T) {
	t.Parallel()

	s := "HTTP/1.1 200\r\n" +
		"WithTabs: \t v1 \t\r\n" + // "v1"
		"WithTabs-Start: \t \t v1 \r\n" + // "v1"
		"WithTabs-End: v1 \t \t\t\t\r\n" + // "v1"
		"WithTabs-Multi-Line: \t v1 \t;\r\n \t v2 \t;\r\n\t v3\r\n" + // "v1 \t; v2 \t; v3"
		"\r\n"

	resHeader := new(ResponseHeader)
	if _, err := resHeader.parse([]byte(s)); err != nil {
		t.Fatalf("parse headers with tabs values failed, %v", err)
	}

	groundTruth := map[string]string{
		"WithTabs":            "v1",
		"WithTabs-Start":      "v1",
		"WithTabs-End":        "v1",
		"WithTabs-Multi-Line": "v1 \t; v2 \t; v3",
	}

	for name, want := range groundTruth {
		if got := b2s(resHeader.Peek(name)); got != want {
			t.Errorf("ResponseHeader.parser() unexpected %q got: %q want: %q", name, got, want)
		}
	}

	s = "GET / HTTP/1.1\r\n" +
		"WithTabs: \t v1 \t\r\n" + // "v1"
		"WithTabs-Start: \t \t v1 \r\n" + // "v1"
		"WithTabs-End: v1 \t \t\t\t\r\n" + // "v1"
		"WithTabs-Multi-Line: \t v1 \t;\r\n \t v2 \t;\r\n\t v3\r\n" + // "v1 \t; v2 \t; v3"
		"\r\n"

	reqHeader := new(RequestHeader)
	if _, err := reqHeader.parse([]byte(s)); err != nil {
		t.Fatalf("parse headers with tabs values failed, %v", err)
	}

	for name, want := range groundTruth {
		if got := b2s(reqHeader.Peek(name)); got != want {
			t.Errorf("RequestHeader.parser() unexpected %q got: %q want: %q", name, got, want)
		}
	}
}

func TestResponseHeaderMultiLineName(t *testing.T) {
	t.Parallel()

	s := "HTTP/1.1 200 OK\r\n" +
		"Host: go.dev\r\n" +
		"Gopher-New-\r\n" +
		" Line: This is a header on multiple lines\r\n" +
		"\r\n"
	header := new(ResponseHeader)
	if _, err := header.parse([]byte(s)); err != errInvalidName {
		m := make(map[string]string)
		for k, v := range header.All() {
			m[string(k)] = string(v)
		}
		t.Errorf("expected error, got %q (%v)", m, err)
	}

	if !bytes.Equal(header.StatusMessage(), []byte("OK")) {
		t.Errorf("expected default status line, got: %q", header.StatusMessage())
	}

	if !bytes.Equal(header.Protocol(), []byte("HTTP/1.1")) {
		t.Errorf("expected default protocol, got: %q", header.Protocol())
	}

	if !bytes.Equal(header.appendStatusLine(nil), []byte("HTTP/1.1 200 OK\r\n")) {
		t.Errorf("parse status line with non-default value failed, got: %q want: HTTP/1.1 200 OK", header.Protocol())
	}
}

func TestResponseHeaderMultiLinePanicked(t *testing.T) {
	t.Parallel()

	// Input generated by fuzz testing that caused the parser to panic.
	s, _ := base64.StdEncoding.DecodeString("aAEAIDoKKDoKICA6CgkKCiA6CiA6CgkpCiA6CiA6CiA6Cig6CiAgOgoJCgogOgogOgoJKQogOgogOgogOgogOgogOgoJOg86CiA6CiA6Cig6CiAyCg==")
	header := new(RequestHeader)
	if _, err := header.parse(s); err == nil {
		t.Error("expected error, got <nil>")
	}
}

func TestRequestHeaderLooseBackslashR(t *testing.T) {
	t.Parallel()

	s := "GET / HTTP/1.1\r\n" +
		"Host: go.dev\r\n" +
		"\rFoo: bar\r\n" +
		"\r\n"
	header := new(RequestHeader)
	if _, err := header.parse([]byte(s)); err == nil {
		t.Fatal("expected error, got <nil>")
	}
}

func TestResponseHeaderEmptyValueFromHeader(t *testing.T) {
	t.Parallel()

	var h1 ResponseHeader
	h1.SetContentType("foo/bar")
	h1.Set("EmptyValue1", "")
	h1.Set("EmptyValue2", " ")
	s := h1.String()

	var h ResponseHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(h.ContentType(), h1.ContentType()) {
		t.Fatalf("unexpected content-type: %q. Expecting %q", h.ContentType(), h1.ContentType())
	}
	v1 := h.Peek("EmptyValue1")
	if len(v1) > 0 {
		t.Fatalf("expecting empty value. Got %q", v1)
	}
	v2 := h.Peek("EmptyValue2")
	if len(v2) > 0 {
		t.Fatalf("expecting empty value. Got %q", v2)
	}
}

func TestResponseHeaderEmptyValueFromString(t *testing.T) {
	t.Parallel()

	s := "HTTP/1.1 200 OK\r\n" +
		"EmptyValue1:\r\n" +
		"Content-Type: foo/bar\r\n" +
		"EmptyValue2: \r\n" +
		"\r\n"

	var h ResponseHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(h.ContentType()) != "foo/bar" {
		t.Fatalf("unexpected content-type: %q. Expecting %q", h.ContentType(), "foo/bar")
	}
	v1 := h.Peek("EmptyValue1")
	if len(v1) > 0 {
		t.Fatalf("expecting empty value. Got %q", v1)
	}
	v2 := h.Peek("EmptyValue2")
	if len(v2) > 0 {
		t.Fatalf("expecting empty value. Got %q", v2)
	}
}

func TestRequestHeaderEmptyValueFromHeader(t *testing.T) {
	t.Parallel()

	var h1 RequestHeader
	h1.SetRequestURI("/foo/bar")
	h1.SetHost("foobar")
	h1.Set("EmptyValue1", "")
	h1.Set("EmptyValue2", " ")
	s := h1.String()

	var h RequestHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(h.Host(), h1.Host()) {
		t.Fatalf("unexpected host: %q. Expecting %q", h.Host(), h1.Host())
	}
	v1 := h.Peek("EmptyValue1")
	if len(v1) > 0 {
		t.Fatalf("expecting empty value. Got %q", v1)
	}
	v2 := h.Peek("EmptyValue2")
	if len(v2) > 0 {
		t.Fatalf("expecting empty value. Got %q", v2)
	}
}

func TestRequestHeaderEmptyValueFromString(t *testing.T) {
	t.Parallel()

	s := "GET / HTTP/1.1\r\n" +
		"EmptyValue1:\r\n" +
		"Host: foobar\r\n" +
		"EmptyValue2: \r\n" +
		"\r\n"
	var h RequestHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(h.Host()) != "foobar" {
		t.Fatalf("unexpected host: %q. Expecting %q", h.Host(), "foobar")
	}
	v1 := h.Peek("EmptyValue1")
	if len(v1) > 0 {
		t.Fatalf("expecting empty value. Got %q", v1)
	}
	v2 := h.Peek("EmptyValue2")
	if len(v2) > 0 {
		t.Fatalf("expecting empty value. Got %q", v2)
	}
}

func TestRequestRawHeaders(t *testing.T) {
	t.Parallel()

	kvs := "hOsT: foobar\r\n" +
		"value:  b\r\n" +
		"uSeR agent: agent\r\n" +
		"\r\n"
	t.Run("normalized", func(t *testing.T) {
		s := "GET / HTTP/1.1\r\n" + kvs
		exp := kvs
		var h RequestHeader
		br := bufio.NewReader(bytes.NewBufferString(s))
		if err := h.Read(br); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(h.Host()) != "foobar" {
			t.Fatalf("unexpected host: %q. Expecting %q", h.Host(), "foobar")
		}
		v2 := h.Peek("Value")
		if !bytes.Equal(v2, []byte{'b'}) {
			t.Fatalf("expecting non empty value. Got %q", v2)
		}
		// We accept invalid headers with a space.
		// See: https://github.com/valyala/fasthttp/issues/1917
		v3 := h.Peek("uSeR agent")
		if !bytes.Equal(v3, []byte("agent")) {
			t.Fatalf("expecting non empty value. Got %q", v3)
		}
		if raw := h.RawHeaders(); string(raw) != exp {
			t.Fatalf("expected header %q, got %q", exp, raw)
		}
	})
	for _, n := range []int{0, 1, 4, 8} {
		t.Run(fmt.Sprintf("post-%dk", n), func(t *testing.T) {
			l := 1024 * n
			body := make([]byte, l)
			for i := range body {
				body[i] = 'a'
			}
			cl := fmt.Sprintf("Content-Length: %d\r\n", l)
			s := "POST / HTTP/1.1\r\n" + cl + kvs + string(body)
			exp := cl + kvs
			var h RequestHeader
			br := bufio.NewReader(bytes.NewBufferString(s))
			if err := h.Read(br); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(h.Host()) != "foobar" {
				t.Fatalf("unexpected host: %q. Expecting %q", h.Host(), "foobar")
			}
			v2 := h.Peek("Value")
			if !bytes.Equal(v2, []byte{'b'}) {
				t.Fatalf("expecting non empty value. Got %q", v2)
			}
			if raw := h.RawHeaders(); string(raw) != exp {
				t.Fatalf("expected header %q, got %q", exp, raw)
			}
		})
	}
	t.Run("http10", func(t *testing.T) {
		s := "GET / HTTP/1.0\r\n" + kvs
		exp := kvs
		var h RequestHeader
		br := bufio.NewReader(bytes.NewBufferString(s))
		if err := h.Read(br); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(h.Host()) != "foobar" {
			t.Fatalf("unexpected host: %q. Expecting %q", h.Host(), "foobar")
		}
		v2 := h.Peek("Value")
		if !bytes.Equal(v2, []byte{'b'}) {
			t.Fatalf("expecting non empty value. Got %q", v2)
		}
		if raw := h.RawHeaders(); string(raw) != exp {
			t.Fatalf("expected header %q, got %q", exp, raw)
		}
	})
	t.Run("no-kvs", func(t *testing.T) {
		s := "GET / HTTP/1.1\r\n\r\n"
		exp := ""
		var h RequestHeader
		h.DisableNormalizing()
		br := bufio.NewReader(bytes.NewBufferString(s))
		if err := h.Read(br); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(h.Host()) != 0 {
			t.Fatalf("unexpected host: %q. Expecting %q", h.Host(), "")
		}
		v1 := h.Peek("NoKey")
		if len(v1) > 0 {
			t.Fatalf("expecting empty value. Got %q", v1)
		}
		if raw := h.RawHeaders(); string(raw) != exp {
			t.Fatalf("expected header %q, got %q", exp, raw)
		}
	})
}

func TestRequestDisableSpecialHeaders(t *testing.T) {
	t.Parallel()

	// Test original header functionality
	kvs := "Host: foobar\r\n" +
		"User-Agent: ua\r\n" +
		"Non-Special: val\r\n" +
		"\r\n"

	var h RequestHeader
	h.DisableSpecialHeader()

	s := "GET / HTTP/1.0\r\n" + kvs
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// assert order of all headers preserved
	if h.String() != s {
		t.Fatalf("Headers not equal: %q. Expecting %q", h.String(), s)
	}
	h.SetCanonical([]byte("host"), []byte("notfoobar"))
	if string(h.Host()) != "foobar" {
		t.Fatalf("unexpected: %q. Expecting %q", h.Host(), "foobar")
	}
	if h.String() != "GET / HTTP/1.0\r\nHost: foobar\r\nUser-Agent: ua\r\nNon-Special: val\r\nhost: notfoobar\r\n\r\n" {
		t.Fatalf("custom special header ordering failed: %q", h.String())
	}

	// Test body parsing with DisableSpecialHeader - should work correctly after fix
	testBody := "a=b&test=123"
	rawRequest := "POST /test HTTP/1.1\r\n" +
		"Host: example.com\r\n" +
		"Content-Type: application/x-www-form-urlencoded\r\n" +
		"Content-Length: " + strconv.Itoa(len(testBody)) + "\r\n" +
		"\r\n" +
		testBody

	var req Request
	req.Header.DisableSpecialHeader()

	br2 := bufio.NewReader(bytes.NewBufferString(rawRequest))
	if err := req.ReadLimitBody(br2, 0); err != nil {
		t.Fatalf("unexpected error reading request: %v", err)
	}

	// Verify Content-Length is correctly parsed with DisableSpecialHeader
	if req.Header.ContentLength() != len(testBody) {
		t.Fatalf("ContentLength() incorrect with DisableSpecialHeader: got %d, expected %d",
			req.Header.ContentLength(), len(testBody))
	}

	// Verify body is preserved with DisableSpecialHeader
	if string(req.Body()) != testBody {
		t.Fatalf("body content incorrect with DisableSpecialHeader: got %q, expected %q",
			string(req.Body()), testBody)
	}
}

func TestRequestDisableSpecialHeadersChunked(t *testing.T) {
	t.Parallel()

	testBody := "chunked-test"
	rawRequest := "POST /test HTTP/1.1\r\n" +
		"Host: example.com\r\n" +
		"Transfer-Encoding: chunked\r\n" +
		"\r\n" +
		"c\r\n" +
		testBody + "\r\n" +
		"0\r\n\r\n"

	var req Request
	req.Header.DisableSpecialHeader()

	br := bufio.NewReader(bytes.NewBufferString(rawRequest))
	if err := req.ReadLimitBody(br, 0); err != nil {
		t.Fatalf("unexpected error reading chunked request: %v", err)
	}

	// Verify chunked encoding is detected
	if req.Header.ContentLength() != -1 {
		t.Fatalf("chunked encoding not detected with DisableSpecialHeader: got %d, expected -1",
			req.Header.ContentLength())
	}

	// Verify chunked body is preserved
	if string(req.Body()) != testBody {
		t.Fatalf("chunked body incorrect with DisableSpecialHeader: got %q, expected %q",
			string(req.Body()), testBody)
	}
}

func TestRequestDisableSpecialHeadersIdentity(t *testing.T) {
	t.Parallel()

	rawRequest := "GET /test HTTP/1.1\r\n" +
		"Host: example.com\r\n" +
		"\r\n"

	var req Request
	req.Header.DisableSpecialHeader()

	br := bufio.NewReader(bytes.NewBufferString(rawRequest))
	if err := req.ReadLimitBody(br, 0); err != nil {
		t.Fatalf("unexpected error reading identity request: %v", err)
	}

	// Verify identity encoding is detected
	if req.Header.ContentLength() != -2 {
		t.Fatalf("identity encoding not detected with DisableSpecialHeader: got %d, expected -2",
			req.Header.ContentLength())
	}
}

func TestRequestHeaderSetCookieWithSpecialChars(t *testing.T) {
	t.Parallel()

	var h RequestHeader
	h.Set("Cookie", "ID&14")
	s := h.String()

	if !strings.Contains(s, "Cookie: ID&14") {
		t.Fatalf("Missing cookie in request header: %q", s)
	}

	var h1 RequestHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := h1.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cookie := h1.Peek(HeaderCookie)
	if string(cookie) != "ID&14" {
		t.Fatalf("unexpected cooke: %q. Expecting %q", cookie, "ID&14")
	}

	cookie = h1.Cookie("")
	if string(cookie) != "ID&14" {
		t.Fatalf("unexpected cooke: %q. Expecting %q", cookie, "ID&14")
	}
}

func TestResponseHeaderDefaultStatusCode(t *testing.T) {
	t.Parallel()

	var h ResponseHeader
	statusCode := h.StatusCode()
	if statusCode != StatusOK {
		t.Fatalf("unexpected status code: %d. Expecting %d", statusCode, StatusOK)
	}
}

func TestResponseHeaderDelClientCookie(t *testing.T) {
	t.Parallel()

	cookieName := "foobar"

	var h ResponseHeader
	c := AcquireCookie()
	c.SetKey(cookieName)
	c.SetValue("aasdfsdaf")
	h.SetCookie(c)

	h.DelClientCookieBytes([]byte(cookieName))
	if !h.Cookie(c) {
		t.Fatalf("expecting cookie %q", c.Key())
	}
	if !c.Expire().Equal(CookieExpireDelete) {
		t.Fatalf("unexpected cookie expiration time: %q. Expecting %q", c.Expire(), CookieExpireDelete)
	}
	if len(c.Value()) > 0 {
		t.Fatalf("unexpected cookie value: %q. Expecting empty value", c.Value())
	}
	ReleaseCookie(c)
}

func TestResponseHeaderAdd(t *testing.T) {
	t.Parallel()

	m := make(map[string]struct{})
	var h ResponseHeader
	h.Add("aaa", "bbb")
	h.Add("content-type", "xxx")
	m["bbb"] = struct{}{}
	m["xxx"] = struct{}{}
	for i := 0; i < 10; i++ {
		v := strconv.Itoa(i)
		h.Add("Foo-Bar", v)
		m[v] = struct{}{}
	}
	if h.Len() != 12 {
		t.Fatalf("unexpected header len %d. Expecting 12", h.Len())
	}

	for k, v := range h.All() {
		switch string(k) {
		case "Aaa", "Foo-Bar", "Content-Type":
			if _, ok := m[string(v)]; !ok {
				t.Fatalf("unexpected value found %q. key %q", v, k)
			}
			delete(m, string(v))
		default:
			t.Fatalf("unexpected key found: %q", k)
		}
	}
	if len(m) > 0 {
		t.Fatalf("%d headers are missed", len(m))
	}

	s := h.String()
	br := bufio.NewReader(bytes.NewBufferString(s))
	var h1 ResponseHeader
	if err := h1.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for k, v := range h.All() {
		switch string(k) {
		case "Aaa", "Foo-Bar", "Content-Type":
			m[string(v)] = struct{}{}
		default:
			t.Fatalf("unexpected key found: %q", k)
		}
	}
	if len(m) != 12 {
		t.Fatalf("unexpected number of headers: %d. Expecting 12", len(m))
	}
}

func TestRequestHeaderAdd(t *testing.T) {
	t.Parallel()

	m := make(map[string]struct{})
	var h RequestHeader
	h.Add("aaa", "bbb")
	h.Add("user-agent", "xxx")
	m["bbb"] = struct{}{}
	m["xxx"] = struct{}{}
	for i := 0; i < 10; i++ {
		v := strconv.Itoa(i)
		h.Add("Foo-Bar", v)
		m[v] = struct{}{}
	}
	if h.Len() != 12 {
		t.Fatalf("unexpected header len %d. Expecting 12", h.Len())
	}

	for k, v := range h.All() {
		switch string(k) {
		case "Aaa", "Foo-Bar", "User-Agent":
			if _, ok := m[string(v)]; !ok {
				t.Fatalf("unexpected value found %q. key %q", v, k)
			}
			delete(m, string(v))
		default:
			t.Fatalf("unexpected key found: %q", k)
		}
	}
	if len(m) > 0 {
		t.Fatalf("%d headers are missed", len(m))
	}

	s := h.String()
	br := bufio.NewReader(bytes.NewBufferString(s))
	var h1 RequestHeader
	if err := h1.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for k, v := range h.All() {
		switch string(k) {
		case "Aaa", "Foo-Bar", "User-Agent":
			m[string(v)] = struct{}{}
		default:
			t.Fatalf("unexpected key found: %q", k)
		}
	}
	if len(m) != 12 {
		t.Fatalf("unexpected number of headers: %d. Expecting 12", len(m))
	}
	s1 := h1.String()
	if s != s1 {
		t.Fatalf("unexpected headers %q. Expecting %q", s1, s)
	}
}

func TestHasHeaderValue(t *testing.T) {
	t.Parallel()

	testHasHeaderValue(t, "foobar", "foobar", true)
	testHasHeaderValue(t, "foobar", "foo", false)
	testHasHeaderValue(t, "foobar", "bar", false)
	testHasHeaderValue(t, "keep-alive, Upgrade", "keep-alive", true)
	testHasHeaderValue(t, "keep-alive  ,    Upgrade", "Upgrade", true)
	testHasHeaderValue(t, "keep-alive, Upgrade", "Upgrade-foo", false)
	testHasHeaderValue(t, "keep-alive, Upgrade", "Upgr", false)
	testHasHeaderValue(t, "foo  ,   bar,  baz   ,", "foo", true)
	testHasHeaderValue(t, "foo  ,   bar,  baz   ,", "bar", true)
	testHasHeaderValue(t, "foo  ,   bar,  baz   ,", "baz", true)
	testHasHeaderValue(t, "foo  ,   bar,  baz   ,", "ba", false)
	testHasHeaderValue(t, "foo, ", "", true)
	testHasHeaderValue(t, "foo", "", false)
}

func testHasHeaderValue(t *testing.T, s, value string, has bool) {
	ok := hasHeaderValue([]byte(s), []byte(value))
	if ok != has {
		t.Fatalf("unexpected hasHeaderValue(%q, %q)=%v. Expecting %v", s, value, ok, has)
	}
}

func TestRequestHeaderDel(t *testing.T) {
	t.Parallel()

	var h RequestHeader
	h.Set("Foo-Bar", "baz")
	h.Set("aaa", "bbb")
	h.Set(HeaderConnection, "keep-alive")
	h.Set("Content-Type", "aaa")
	h.Set(HeaderHost, "aaabbb")
	h.Set("User-Agent", "asdfas")
	h.Set("Content-Length", "1123")
	h.Set("Cookie", "foobar=baz")
	h.Set(HeaderTrailer, "foo, bar")

	h.Del("foo-bar")
	h.Del("connection")
	h.DelBytes([]byte("content-type"))
	h.Del("Host")
	h.Del("user-agent")
	h.Del("content-length")
	h.Del("cookie")
	h.Del("trailer")

	hv := h.Peek("aaa")
	if string(hv) != "bbb" {
		t.Fatalf("unexpected header value: %q. Expecting %q", hv, "bbb")
	}
	hv = h.Peek("Foo-Bar")
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}
	hv = h.Peek(HeaderConnection)
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}
	hv = h.Peek(HeaderContentType)
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}
	hv = h.Peek(HeaderHost)
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}
	hv = h.Peek(HeaderUserAgent)
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}
	hv = h.Peek(HeaderContentLength)
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}
	hv = h.Peek(HeaderCookie)
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}
	hv = h.Peek(HeaderTrailer)
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}

	cv := h.Cookie("foobar")
	if len(cv) > 0 {
		t.Fatalf("unexpected cookie obtained: %q", cv)
	}
	if h.ContentLength() != 0 {
		t.Fatalf("unexpected content-length: %d. Expecting 0", h.ContentLength())
	}
}

func TestResponseHeaderDel(t *testing.T) {
	t.Parallel()

	var h ResponseHeader
	h.Set("Foo-Bar", "baz")
	h.Set("aaa", "bbb")
	h.Set(HeaderConnection, "keep-alive")
	h.Set(HeaderContentType, "aaa")
	h.Set(HeaderContentEncoding, "gzip")
	h.Set(HeaderServer, "aaabbb")
	h.Set(HeaderContentLength, "1123")
	h.Set(HeaderTrailer, "foo, bar")

	var c Cookie
	c.SetKey("foo")
	c.SetValue("bar")
	h.SetCookie(&c)

	h.Del("foo-bar")
	h.Del("connection")
	h.DelBytes([]byte("content-type"))
	h.Del(HeaderServer)
	h.Del("content-length")
	h.Del("set-cookie")
	h.Del("trailer")

	hv := h.Peek("aaa")
	if string(hv) != "bbb" {
		t.Fatalf("unexpected header value: %q. Expecting %q", hv, "bbb")
	}
	hv = h.Peek("Foo-Bar")
	if len(hv) > 0 {
		t.Fatalf("non-zero header value: %q", hv)
	}
	hv = h.Peek(HeaderConnection)
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}
	hv = h.Peek(HeaderContentType)
	if !bytes.Equal(hv, defaultContentType) {
		t.Fatalf("unexpected content-type: %q. Expecting %q", hv, defaultContentType)
	}
	hv = h.Peek(HeaderContentEncoding)
	if string(hv) != "gzip" {
		t.Fatalf("unexpected content-encoding: %q. Expecting %q", hv, "gzip")
	}
	hv = h.Peek(HeaderServer)
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}
	hv = h.Peek(HeaderContentLength)
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}
	hv = h.Peek(HeaderTrailer)
	if len(hv) > 0 {
		t.Fatalf("non-zero value: %q", hv)
	}

	if h.Cookie(&c) {
		t.Fatalf("unexpected cookie obtained: %q", &c)
	}
	if h.ContentLength() != 0 {
		t.Fatalf("unexpected content-length: %d. Expecting 0", h.ContentLength())
	}
}

func TestResponseHeaderSetTrailerGetBytes(t *testing.T) {
	t.Parallel()

	h := &ResponseHeader{}
	h.noDefaultDate = true
	h.Set("Foo", "bar")
	h.Set(HeaderTrailer, "Baz")
	h.Set("Baz", "test")

	headerBytes := h.Header()
	n, err := h.parseFirstLine(headerBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(headerBytes[n:]) != "Foo: bar\r\nTrailer: Baz\r\n\r\n" {
		t.Fatalf("Unexpected header: %q. Expected %q", headerBytes[n:], "Foo: bar\nTrailer: Baz\n\n")
	}
	if string(h.TrailerHeader()) != "Baz: test\r\n\r\n" {
		t.Fatalf("Unexpected trailer header: %q. Expected %q", h.TrailerHeader(), "Baz: test\r\n\r\n")
	}
}

func TestRequestHeaderSetTrailerGetBytes(t *testing.T) {
	t.Parallel()

	h := &RequestHeader{}
	h.Set("Foo", "bar")
	h.Set(HeaderTrailer, "Baz")
	h.Set("Baz", "test")

	headerBytes := h.Header()
	n, err := h.parseFirstLine(headerBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(headerBytes[n:]) != "Foo: bar\r\nTrailer: Baz\r\n\r\n" {
		t.Fatalf("Unexpected header: %q. Expected %q", headerBytes[n:], "Foo: bar\nTrailer: Baz\n\n")
	}
	if string(h.TrailerHeader()) != "Baz: test\r\n\r\n" {
		t.Fatalf("Unexpected trailer header: %q. Expected %q", h.TrailerHeader(), "Baz: test\r\n\r\n")
	}
}

func TestAppendNormalizedHeaderKeyBytes(t *testing.T) {
	t.Parallel()

	testAppendNormalizedHeaderKeyBytes(t, "", "")
	testAppendNormalizedHeaderKeyBytes(t, "Content-Type", "Content-Type")
	testAppendNormalizedHeaderKeyBytes(t, "foO-bAr-BAZ", "Foo-Bar-Baz")
}

func testAppendNormalizedHeaderKeyBytes(t *testing.T, key, expectedKey string) {
	buf := []byte("foobar")
	result := AppendNormalizedHeaderKeyBytes(buf, []byte(key))
	normalizedKey := result[len(buf):]
	if string(normalizedKey) != expectedKey {
		t.Fatalf("unexpected normalized key %q. Expecting %q", normalizedKey, expectedKey)
	}
}

func TestRequestHeaderHTTP10ConnectionClose(t *testing.T) {
	t.Parallel()

	s := "GET / HTTP/1.0\r\nHost: foobar\r\n\r\n"
	var h RequestHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !h.ConnectionClose() {
		t.Fatalf("expecting 'Connection: close' request header")
	}
}

func TestRequestHeaderHTTP10ConnectionKeepAlive(t *testing.T) {
	t.Parallel()

	s := "GET / HTTP/1.0\r\nHost: foobar\r\nConnection: keep-alive\r\n\r\n"
	var h RequestHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.ConnectionClose() {
		t.Fatalf("unexpected 'Connection: close' request header")
	}
}

func TestBufferSnippet(t *testing.T) {
	t.Parallel()

	testBufferSnippet(t, "", `""`)
	testBufferSnippet(t, "foobar", `"foobar"`)

	b := string(createFixedBody(199))
	bExpected := fmt.Sprintf("%q", b)
	testBufferSnippet(t, b, bExpected)
	for i := 0; i < 10; i++ {
		b += "foobar"
		bExpected = fmt.Sprintf("%q", b)
		testBufferSnippet(t, b, bExpected)
	}

	b = string(createFixedBody(400))
	bExpected = fmt.Sprintf("%q", b)
	testBufferSnippet(t, b, bExpected)
	for i := 0; i < 10; i++ {
		b += "sadfqwer"
		bExpected = fmt.Sprintf("%q...%q", b[:200], b[len(b)-200:])
		testBufferSnippet(t, b, bExpected)
	}
}

func testBufferSnippet(t *testing.T, buf, expectedSnippet string) {
	snippet := bufferSnippet([]byte(buf))
	if snippet != expectedSnippet {
		t.Fatalf("unexpected snippet %q. Expecting %q", snippet, expectedSnippet)
	}
}

func TestResponseHeaderTrailingCRLFSuccess(t *testing.T) {
	t.Parallel()

	trailingCRLF := "\r\n\r\n\r\n"
	s := "HTTP/1.1 200 OK\r\nContent-Type: aa\r\nContent-Length: 123\r\n\r\n" + trailingCRLF

	var r ResponseHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := r.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// try reading the trailing CRLF. It must return EOF
	err := r.Read(br)
	if err == nil {
		t.Fatalf("expecting error")
	}
	if err != io.EOF {
		t.Fatalf("unexpected error: %v. Expecting %v", err, io.EOF)
	}
}

func TestResponseHeaderTrailingCRLFError(t *testing.T) {
	t.Parallel()

	trailingCRLF := "\r\nerror\r\n\r\n"
	s := "HTTP/1.1 200 OK\r\nContent-Type: aa\r\nContent-Length: 123\r\n\r\n" + trailingCRLF

	var r ResponseHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := r.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// try reading the trailing CRLF. It must return EOF
	err := r.Read(br)
	if err == nil {
		t.Fatalf("expecting error")
	}
	if err == io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequestHeaderTrailingCRLFSuccess(t *testing.T) {
	t.Parallel()

	trailingCRLF := "\r\n\r\n\r\n"
	s := "GET / HTTP/1.1\r\nHost: aaa.com\r\n\r\n" + trailingCRLF

	var r RequestHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := r.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// try reading the trailing CRLF. It must return EOF
	err := r.Read(br)
	if err == nil {
		t.Fatalf("expecting error")
	}
	if err != io.EOF {
		t.Fatalf("unexpected error: %v. Expecting %v", err, io.EOF)
	}
}

func TestRequestHeaderTrailingCRLFError(t *testing.T) {
	t.Parallel()

	trailingCRLF := "\r\nerror\r\n\r\n"
	s := "GET / HTTP/1.1\r\nHost: aaa.com\r\n\r\n" + trailingCRLF

	var r RequestHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := r.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// try reading the trailing CRLF. It must return EOF
	err := r.Read(br)
	if err == nil {
		t.Fatalf("expecting error")
	}
	if err == io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequestHeaderReadEOF(t *testing.T) {
	t.Parallel()

	var r RequestHeader

	br := bufio.NewReader(&bytes.Buffer{})
	err := r.Read(br)
	if err == nil {
		t.Fatalf("expecting error")
	}
	if err != io.EOF {
		t.Fatalf("unexpected error: %v. Expecting %v", err, io.EOF)
	}

	// incomplete request header mustn't return io.EOF
	br = bufio.NewReader(bytes.NewBufferString("GET "))
	err = r.Read(br)
	if err == nil {
		t.Fatalf("expecting error")
	}
	if err == io.EOF {
		t.Fatalf("expecting non-EOF error")
	}
}

func TestResponseHeaderReadEOF(t *testing.T) {
	t.Parallel()

	var r ResponseHeader

	br := bufio.NewReader(&bytes.Buffer{})
	err := r.Read(br)
	if err == nil {
		t.Fatalf("expecting error")
	}
	if err != io.EOF {
		t.Fatalf("unexpected error: %v. Expecting %v", err, io.EOF)
	}

	// incomplete response header mustn't return io.EOF
	br = bufio.NewReader(bytes.NewBufferString("HTTP/1.1 "))
	err = r.Read(br)
	if err == nil {
		t.Fatalf("expecting error")
	}
	if err == io.EOF {
		t.Fatalf("expecting non-EOF error")
	}
}

func TestResponseHeaderOldVersion(t *testing.T) {
	t.Parallel()

	var h ResponseHeader

	s := "HTTP/1.0 200 OK\r\nContent-Length: 5\r\nContent-Type: aaa\r\n\r\n12345"
	s += "HTTP/1.0 200 OK\r\nContent-Length: 2\r\nContent-Type: ass\r\nConnection: keep-alive\r\n\r\n42"
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !h.ConnectionClose() {
		t.Fatalf("expecting 'Connection: close' for the response with old http protocol")
	}

	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.ConnectionClose() {
		t.Fatalf("unexpected 'Connection: close' for keep-alive response with old http protocol")
	}
}

func TestRequestHeaderSetByteRange(t *testing.T) {
	t.Parallel()

	testRequestHeaderSetByteRange(t, 0, 10, "bytes=0-10")
	testRequestHeaderSetByteRange(t, 123, -1, "bytes=123-")
	testRequestHeaderSetByteRange(t, -234, 58349, "bytes=-234")
}

func testRequestHeaderSetByteRange(t *testing.T, startPos, endPos int, expectedV string) {
	var h RequestHeader
	h.SetByteRange(startPos, endPos)
	v := h.Peek(HeaderRange)
	if string(v) != expectedV {
		t.Fatalf("unexpected range: %q. Expecting %q. startPos=%d, endPos=%d", v, expectedV, startPos, endPos)
	}
}

func TestResponseHeaderSetContentRange(t *testing.T) {
	t.Parallel()

	testResponseHeaderSetContentRange(t, 0, 0, 1, "bytes 0-0/1")
	testResponseHeaderSetContentRange(t, 123, 456, 789, "bytes 123-456/789")
}

func testResponseHeaderSetContentRange(t *testing.T, startPos, endPos, contentLength int, expectedV string) {
	var h ResponseHeader
	h.SetContentRange(startPos, endPos, contentLength)
	v := h.Peek(HeaderContentRange)
	if string(v) != expectedV {
		t.Fatalf("unexpected content-range: %q. Expecting %q. startPos=%d, endPos=%d, contentLength=%d",
			v, expectedV, startPos, endPos, contentLength)
	}
}

func TestRequestHeaderHasAcceptEncoding(t *testing.T) {
	t.Parallel()

	testRequestHeaderHasAcceptEncoding(t, "", "gzip", false)
	testRequestHeaderHasAcceptEncoding(t, "gzip", "sdhc", false)
	testRequestHeaderHasAcceptEncoding(t, "deflate", "deflate", true)
	testRequestHeaderHasAcceptEncoding(t, "gzip, deflate, sdhc", "gzi", false)
	testRequestHeaderHasAcceptEncoding(t, "gzip, deflate, sdhc", "dhc", false)
	testRequestHeaderHasAcceptEncoding(t, "gzip, deflate, sdhc", "sdh", false)
	testRequestHeaderHasAcceptEncoding(t, "gzip, deflate, sdhc", "zip", false)
	testRequestHeaderHasAcceptEncoding(t, "gzip, deflate, sdhc", "flat", false)
	testRequestHeaderHasAcceptEncoding(t, "gzip, deflate, sdhc", "flate", false)
	testRequestHeaderHasAcceptEncoding(t, "gzip, deflate, sdhc", "def", false)
	testRequestHeaderHasAcceptEncoding(t, "gzip, deflate, sdhc", "gzip", true)
	testRequestHeaderHasAcceptEncoding(t, "gzip, deflate, sdhc", "deflate", true)
	testRequestHeaderHasAcceptEncoding(t, "gzip, deflate, sdhc", "sdhc", true)
}

func testRequestHeaderHasAcceptEncoding(t *testing.T, ae, v string, resultExpected bool) {
	var h RequestHeader
	h.Set(HeaderAcceptEncoding, ae)
	result := h.HasAcceptEncoding(v)
	if result != resultExpected {
		t.Fatalf("unexpected result in HasAcceptEncoding(%q, %q): %v. Expecting %v", ae, v, result, resultExpected)
	}
}

func TestVisitHeaderParams(t *testing.T) {
	t.Parallel()
	testVisitHeaderParams(t, "text/plain;charset=utf-8;q=0.39", [][2]string{{"charset", "utf-8"}, {"q", "0.39"}})
	testVisitHeaderParams(t, "text/plain;   foo=bar   ;", [][2]string{{"foo", "bar"}})
	testVisitHeaderParams(t, `text/plain;      foo="bar";   `, [][2]string{{"foo", "bar"}})
	testVisitHeaderParams(t, `text/plain; foo="text/plain,text/html;charset=\"utf-8\""`, [][2]string{{"foo", `text/plain,text/html;charset=\"utf-8\"`}})
	testVisitHeaderParams(t, "text/plain foo=bar", [][2]string{})
	testVisitHeaderParams(t, "text/plain;", [][2]string{})
	testVisitHeaderParams(t, "text/plain; ", [][2]string{})
	testVisitHeaderParams(t, "text/plain; foo", [][2]string{})
	testVisitHeaderParams(t, "text/plain; foo=", [][2]string{})
	testVisitHeaderParams(t, "text/plain; =bar", [][2]string{})
	testVisitHeaderParams(t, "text/plain; foo = bar", [][2]string{})
	testVisitHeaderParams(t, `text/plain; foo="bar`, [][2]string{})
	testVisitHeaderParams(t, "text/plain;;foo=bar", [][2]string{})

	parsed := make([][2]string, 0)
	VisitHeaderParams([]byte(`text/plain; foo=bar; charset=utf-8`), func(key, value []byte) bool {
		parsed = append(parsed, [2]string{string(key), string(value)})
		return !bytes.Equal(key, []byte("foo"))
	})

	if len(parsed) != 1 {
		t.Fatalf("expected 1 HTTP parameter, parsed %v", len(parsed))
	}

	if parsed[0] != [2]string{"foo", "bar"} {
		t.Fatalf("unexpected parameter %v=%v. Expecting foo=bar", parsed[0][0], parsed[0][1])
	}
}

func testVisitHeaderParams(t *testing.T, header string, expectedParams [][2]string) {
	parsed := make([][2]string, 0)
	VisitHeaderParams([]byte(header), func(key, value []byte) bool {
		parsed = append(parsed, [2]string{string(key), string(value)})
		return true
	})

	if len(parsed) != len(expectedParams) {
		t.Fatalf("expected %v HTTP parameters, parsed %v", len(expectedParams), len(parsed))
	}

	for i := range expectedParams {
		if expectedParams[i] != parsed[i] {
			t.Fatalf("unexpected parameter %v=%v. Expecting %v=%v", parsed[i][0], parsed[i][1], expectedParams[i][0], expectedParams[i][1])
		}
	}
}

func TestRequestMultipartFormBoundary(t *testing.T) {
	t.Parallel()

	testRequestMultipartFormBoundary(t, "POST / HTTP/1.1\r\nContent-Type: multipart/form-data; boundary=foobar\r\n\r\n", "foobar")

	// incorrect content-type
	testRequestMultipartFormBoundary(t, "POST / HTTP/1.1\r\nContent-Type: foo/bar\r\n\r\n", "")

	// empty boundary
	testRequestMultipartFormBoundary(t, "POST / HTTP/1.1\r\nContent-Type: multipart/form-data; boundary=\r\n\r\n", "")

	// missing boundary
	testRequestMultipartFormBoundary(t, "POST / HTTP/1.1\r\nContent-Type: multipart/form-data\r\n\r\n", "")

	// boundary after other content-type params
	testRequestMultipartFormBoundary(t, "POST / HTTP/1.1\r\nContent-Type: multipart/form-data;   foo=bar;   boundary=--aaabb  \r\n\r\n", "--aaabb")

	// quoted boundary
	testRequestMultipartFormBoundary(t, "POST / HTTP/1.1\r\nContent-Type: multipart/form-data; boundary=\"foobar\"\r\n\r\n", "foobar")

	var h RequestHeader
	h.SetMultipartFormBoundary("foobarbaz")
	b := h.MultipartFormBoundary()
	if string(b) != "foobarbaz" {
		t.Fatalf("unexpected boundary %q. Expecting %q", b, "foobarbaz")
	}
}

func testRequestMultipartFormBoundary(t *testing.T, s, boundary string) {
	var h RequestHeader
	r := bytes.NewBufferString(s)
	br := bufio.NewReader(r)
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v. s=%q, boundary=%q", err, s, boundary)
	}

	b := h.MultipartFormBoundary()
	if string(b) != boundary {
		t.Fatalf("unexpected boundary %q. Expecting %q. s=%q", b, boundary, s)
	}
}

func TestResponseHeaderConnectionUpgrade(t *testing.T) {
	t.Parallel()

	testResponseHeaderConnectionUpgrade(t, "HTTP/1.1 200 OK\r\nContent-Length: 10\r\nConnection: Upgrade, HTTP2-Settings\r\n\r\n",
		true, true)
	testResponseHeaderConnectionUpgrade(t, "HTTP/1.1 200 OK\r\nContent-Length: 10\r\nConnection: keep-alive, Upgrade\r\n\r\n",
		true, true)

	// non-http/1.1 protocol has 'connection: close' by default, which also disables 'connection: upgrade'
	testResponseHeaderConnectionUpgrade(t, "HTTP/1.0 200 OK\r\nContent-Length: 10\r\nConnection: Upgrade, HTTP2-Settings\r\n\r\n",
		false, false)

	// explicit keep-alive for non-http/1.1, so 'connection: upgrade' works
	testResponseHeaderConnectionUpgrade(t, "HTTP/1.0 200 OK\r\nContent-Length: 10\r\nConnection: Upgrade, keep-alive\r\n\r\n",
		true, true)

	// implicit keep-alive for http/1.1
	testResponseHeaderConnectionUpgrade(t, "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n", false, true)

	// no content-length, so 'connection: close' is assumed
	testResponseHeaderConnectionUpgrade(t, "HTTP/1.1 200 OK\r\n\r\n", false, false)
}

func testResponseHeaderConnectionUpgrade(t *testing.T, s string, isUpgrade, isKeepAlive bool) {
	var h ResponseHeader

	r := bytes.NewBufferString(s)
	br := bufio.NewReader(r)
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v. Response header %q", err, s)
	}
	upgrade := h.ConnectionUpgrade()
	if upgrade != isUpgrade {
		t.Fatalf("unexpected 'connection: upgrade' when parsing response header: %v. Expecting %v. header %q. v=%q",
			upgrade, isUpgrade, s, h.Peek("Connection"))
	}
	keepAlive := !h.ConnectionClose()
	if keepAlive != isKeepAlive {
		t.Fatalf("unexpected 'connection: keep-alive' when parsing response header: %v. Expecting %v. header %q. v=%q",
			keepAlive, isKeepAlive, s, &h)
	}
}

func TestRequestHeaderConnectionUpgrade(t *testing.T) {
	t.Parallel()

	testRequestHeaderConnectionUpgrade(t, "GET /foobar HTTP/1.1\r\nConnection: Upgrade, HTTP2-Settings\r\nHost: foobar.com\r\n\r\n",
		true, true)
	testRequestHeaderConnectionUpgrade(t, "GET /foobar HTTP/1.1\r\nConnection: keep-alive,Upgrade\r\nHost: foobar.com\r\n\r\n",
		true, true)

	// non-http/1.1 has 'connection: close' by default, which resets 'connection: upgrade'
	testRequestHeaderConnectionUpgrade(t, "GET /foobar HTTP/1.0\r\nConnection: Upgrade, HTTP2-Settings\r\nHost: foobar.com\r\n\r\n",
		false, false)

	// explicit 'connection: keep-alive' in non-http/1.1
	testRequestHeaderConnectionUpgrade(t, "GET /foobar HTTP/1.0\r\nConnection: foo, Upgrade, keep-alive\r\nHost: foobar.com\r\n\r\n",
		true, true)

	// no upgrade
	testRequestHeaderConnectionUpgrade(t, "GET /foobar HTTP/1.1\r\nConnection: Upgradess, foobar\r\nHost: foobar.com\r\n\r\n",
		false, true)
	testRequestHeaderConnectionUpgrade(t, "GET /foobar HTTP/1.1\r\nHost: foobar.com\r\n\r\n",
		false, true)

	// explicit connection close
	testRequestHeaderConnectionUpgrade(t, "GET /foobar HTTP/1.1\r\nConnection: close\r\nHost: foobar.com\r\n\r\n",
		false, false)
}

func testRequestHeaderConnectionUpgrade(t *testing.T, s string, isUpgrade, isKeepAlive bool) {
	var h RequestHeader

	r := bytes.NewBufferString(s)
	br := bufio.NewReader(r)
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v. Request header %q", err, s)
	}
	upgrade := h.ConnectionUpgrade()
	if upgrade != isUpgrade {
		t.Fatalf("unexpected 'connection: upgrade' when parsing request header: %v. Expecting %v. header %q",
			upgrade, isUpgrade, s)
	}
	keepAlive := !h.ConnectionClose()
	if keepAlive != isKeepAlive {
		t.Fatalf("unexpected 'connection: keep-alive' when parsing request header: %v. Expecting %v. header %q",
			keepAlive, isKeepAlive, s)
	}
}

func TestRequestHeaderProxyWithCookie(t *testing.T) {
	t.Parallel()

	// Proxy request header (read it, then write it without touching any headers).
	var h RequestHeader
	r := bytes.NewBufferString("GET /foo HTTP/1.1\r\nFoo: bar\r\nHost: aaa.com\r\nCookie: foo=bar; bazzz=aaaaaaa; x=y\r\nCookie: aqqqqq=123\r\n\r\n")
	br := bufio.NewReader(r)
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	w := &bytes.Buffer{}
	bw := bufio.NewWriter(w)
	if err := h.Write(bw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var h1 RequestHeader
	br.Reset(w)
	if err := h1.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(h1.RequestURI()) != "/foo" {
		t.Fatalf("unexpected requestURI: %q. Expecting %q", h1.RequestURI(), "/foo")
	}
	if string(h1.Host()) != "aaa.com" {
		t.Fatalf("unexpected host: %q. Expecting %q", h1.Host(), "aaa.com")
	}
	if string(h1.Peek("Foo")) != "bar" {
		t.Fatalf("unexpected Foo: %q. Expecting %q", h1.Peek("Foo"), "bar")
	}
	if string(h1.Cookie("foo")) != "bar" {
		t.Fatalf("unexpected cookie foo=%q. Expecting %q", h1.Cookie("foo"), "bar")
	}
	if string(h1.Cookie("bazzz")) != "aaaaaaa" {
		t.Fatalf("unexpected cookie bazzz=%q. Expecting %q", h1.Cookie("bazzz"), "aaaaaaa")
	}
	if string(h1.Cookie("x")) != "y" {
		t.Fatalf("unexpected cookie x=%q. Expecting %q", h1.Cookie("x"), "y")
	}
	if string(h1.Cookie("aqqqqq")) != "123" {
		t.Fatalf("unexpected cookie aqqqqq=%q. Expecting %q", h1.Cookie("aqqqqq"), "123")
	}
}

func TestResponseHeaderFirstByteReadEOF(t *testing.T) {
	t.Parallel()

	var h ResponseHeader

	r := &errorReader{err: errors.New("non-eof error")}
	br := bufio.NewReader(r)
	err := h.Read(br)
	if err == nil {
		t.Fatalf("expecting error")
	}
	if err != io.EOF {
		t.Fatalf("unexpected error %v. Expecting %v", err, io.EOF)
	}
}

type errorReader struct {
	err error
}

func (r *errorReader) Read(p []byte) (int, error) {
	return 0, r.err
}

func TestRequestHeaderEmptyMethod(t *testing.T) {
	t.Parallel()

	var h RequestHeader

	if !h.IsGet() {
		t.Fatalf("empty method must be equivalent to GET")
	}
}

func TestResponseHeaderHTTPVer(t *testing.T) {
	t.Parallel()

	// non-http/1.1
	testResponseHeaderHTTPVer(t, "HTTP/1.0 200 OK\r\nContent-Type: aaa\r\nContent-Length: 123\r\n\r\n", true)
	testResponseHeaderHTTPVer(t, "HTTP/0.9 200 OK\r\nContent-Type: aaa\r\nContent-Length: 123\r\n\r\n", true)
	testResponseHeaderHTTPVer(t, "foobar 200 OK\r\nContent-Type: aaa\r\nContent-Length: 123\r\n\r\n", true)

	// http/1.1
	testResponseHeaderHTTPVer(t, "HTTP/1.1 200 OK\r\nContent-Type: aaa\r\nContent-Length: 123\r\n\r\n", false)
}

func TestRequestHeaderHTTPVer(t *testing.T) {
	t.Parallel()

	// non-http/1.1
	testRequestHeaderHTTPVer(t, "GET / HTTP/1.0\r\nHost: aa.com\r\n\r\n", true)
	testRequestHeaderHTTPVer(t, "GET / HTTP/0.9\r\nHost: aa.com\r\n\r\n", true)

	// http/1.1
	testRequestHeaderHTTPVer(t, "GET / HTTP/1.1\r\nHost: a.com\r\n\r\n", false)
}

func testResponseHeaderHTTPVer(t *testing.T, s string, connectionClose bool) {
	var h ResponseHeader

	r := bytes.NewBufferString(s)
	br := bufio.NewReader(r)
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v. response=%q", err, s)
	}
	if h.ConnectionClose() != connectionClose {
		t.Fatalf("unexpected connectionClose %v. Expecting %v. response=%q", h.ConnectionClose(), connectionClose, s)
	}
}

func testRequestHeaderHTTPVer(t *testing.T, s string, connectionClose bool) {
	t.Helper()

	var h RequestHeader

	r := bytes.NewBufferString(s)
	br := bufio.NewReader(r)
	if err := h.Read(br); err != nil {
		t.Fatalf("unexpected error: %v. request=%q", err, s)
	}
	if h.ConnectionClose() != connectionClose {
		t.Fatalf("unexpected connectionClose %v. Expecting %v. request=%q", h.ConnectionClose(), connectionClose, s)
	}
}

func TestResponseHeaderCopyTo(t *testing.T) {
	t.Parallel()

	var h ResponseHeader

	h.Set(HeaderSetCookie, "foo=bar")
	h.Set(HeaderContentType, "foobar")
	h.Set(HeaderContentEncoding, "gzip")
	h.Set("AAA-BBB", "aaaa")
	h.Set(HeaderTrailer, "foo, bar")

	var h1 ResponseHeader
	h.CopyTo(&h1)
	if !bytes.Equal(h1.Peek("Set-cookie"), h.Peek("Set-Cookie")) {
		t.Fatalf("unexpected cookie %q. Expected %q", h1.Peek("set-cookie"), h.Peek("set-cookie"))
	}
	if !bytes.Equal(h1.Peek(HeaderContentType), h.Peek(HeaderContentType)) {
		t.Fatalf("unexpected content-type %q. Expected %q", h1.Peek("content-type"), h.Peek("content-type"))
	}
	if !bytes.Equal(h1.Peek(HeaderContentEncoding), h.Peek(HeaderContentEncoding)) {
		t.Fatalf("unexpected content-encoding %q. Expected %q", h1.Peek("content-encoding"), h.Peek("content-encoding"))
	}
	if !bytes.Equal(h1.Peek("aaa-bbb"), h.Peek("AAA-BBB")) {
		t.Fatalf("unexpected aaa-bbb %q. Expected %q", h1.Peek("aaa-bbb"), h.Peek("aaa-bbb"))
	}
	if !bytes.Equal(h1.Peek(HeaderTrailer), h.Peek(HeaderTrailer)) {
		t.Fatalf("unexpected trailer %q. Expected %q", h1.Peek(HeaderTrailer), h.Peek(HeaderTrailer))
	}

	// flush buf
	h.bufK = []byte{}
	h.bufV = []byte{}
	h1.bufK = []byte{}
	h1.bufV = []byte{}

	if !reflect.DeepEqual(&h, &h1) {
		t.Fatalf("ResponseHeaderCopyTo fail, src: \n%+v\ndst: \n%+v\n", &h, &h1)
	}
}

func TestRequestHeaderCopyTo(t *testing.T) {
	t.Parallel()

	var h RequestHeader

	h.Set(HeaderCookie, "aa=bb; cc=dd")
	h.Set(HeaderContentType, "foobar")
	h.Set(HeaderContentEncoding, "gzip")
	h.Set(HeaderHost, "aaaa")
	h.Set("aaaxxx", "123")
	h.Set(HeaderTrailer, "foo, bar")
	h.noDefaultContentType = true

	var h1 RequestHeader
	h.CopyTo(&h1)
	if !bytes.Equal(h1.Peek("cookie"), h.Peek(HeaderCookie)) {
		t.Fatalf("unexpected cookie after copying: %q. Expected %q", h1.Peek("cookie"), h.Peek("cookie"))
	}
	if !bytes.Equal(h1.Peek("content-type"), h.Peek(HeaderContentType)) {
		t.Fatalf("unexpected content-type %q. Expected %q", h1.Peek("content-type"), h.Peek("content-type"))
	}
	if !bytes.Equal(h1.Peek("content-encoding"), h.Peek(HeaderContentEncoding)) {
		t.Fatalf("unexpected content-encoding %q. Expected %q", h1.Peek("content-encoding"), h.Peek("content-encoding"))
	}
	if !bytes.Equal(h1.Peek("host"), h.Peek("host")) {
		t.Fatalf("unexpected host %q. Expected %q", h1.Peek("host"), h.Peek("host"))
	}
	if !bytes.Equal(h1.Peek("aaaxxx"), h.Peek("aaaxxx")) {
		t.Fatalf("unexpected aaaxxx %q. Expected %q", h1.Peek("aaaxxx"), h.Peek("aaaxxx"))
	}
	if !bytes.Equal(h1.Peek(HeaderTrailer), h.Peek(HeaderTrailer)) {
		t.Fatalf("unexpected trailer %q. Expected %q", h1.Peek(HeaderTrailer), h.Peek(HeaderTrailer))
	}

	// flush buf
	h.bufK = []byte{}
	h.bufV = []byte{}
	h1.bufK = []byte{}
	h1.bufV = []byte{}

	if !reflect.DeepEqual(&h, &h1) {
		t.Fatalf("RequestHeaderCopyTo fail, src: \n%+v\ndst: \n%+v\n", &h, &h1)
	}
}

func TestResponseContentTypeNoDefaultNotEmpty(t *testing.T) {
	t.Parallel()

	var h ResponseHeader

	h.SetNoDefaultContentType(true)
	h.SetContentLength(5)

	headers := h.String()

	if strings.Contains(headers, "Content-Type: \r\n") {
		t.Fatalf("ResponseContentTypeNoDefaultNotEmpty fail, response: \n%+v\noutcome: \n%q\n", &h, headers)
	}
}

func TestRequestContentTypeDefaultNotEmpty(t *testing.T) {
	t.Parallel()

	var h RequestHeader
	h.SetMethod(MethodPost)
	h.SetContentLength(5)

	w := &bytes.Buffer{}
	bw := bufio.NewWriter(w)
	if err := h.Write(bw); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	var h1 RequestHeader
	br := bufio.NewReader(w)
	if err := h1.Read(br); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if string(h1.contentType) != "application/octet-stream" {
		t.Fatalf("unexpected Content-Type %q. Expecting %q", h1.contentType, "application/octet-stream")
	}
}

func TestRequestContentTypeNoDefault(t *testing.T) {
	t.Parallel()

	var h RequestHeader
	h.SetMethod(MethodDelete)
	h.SetNoDefaultContentType(true)

	w := &bytes.Buffer{}
	bw := bufio.NewWriter(w)
	if err := h.Write(bw); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	var h1 RequestHeader
	br := bufio.NewReader(w)
	if err := h1.Read(br); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(h1.contentType) != 0 {
		t.Fatalf("unexpected Content-Type %q. Expecting %q", h1.contentType, "")
	}
}

func TestResponseDateNoDefaultNotEmpty(t *testing.T) {
	t.Parallel()

	var h ResponseHeader

	h.noDefaultDate = true

	headers := h.String()

	if strings.Contains(headers, "\r\nDate: ") {
		t.Fatalf("ResponseDateNoDefaultNotEmpty fail, response: \n%+v\noutcome: \n%q\n", &h, headers)
	}
}

func TestRequestHeaderConnectionClose(t *testing.T) {
	t.Parallel()

	var h RequestHeader

	h.Set(HeaderConnection, "close")
	h.Set(HeaderHost, "foobar")
	if !h.ConnectionClose() {
		t.Fatalf("connection: close not set")
	}

	var w bytes.Buffer
	bw := bufio.NewWriter(&w)
	if err := h.Write(bw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var h1 RequestHeader
	br := bufio.NewReader(&w)
	if err := h1.Read(br); err != nil {
		t.Fatalf("error when reading request header: %v", err)
	}

	if !h1.ConnectionClose() {
		t.Fatalf("unexpected connection: close value: %v", h1.ConnectionClose())
	}
	if string(h1.Peek(HeaderConnection)) != "close" {
		t.Fatalf("unexpected connection value: %q. Expecting %q", h.Peek("Connection"), "close")
	}
}

func TestRequestHeaderSetCookie(t *testing.T) {
	t.Parallel()

	var h RequestHeader

	h.Set("Cookie", "foo=bar; baz=aaa")
	h.Set("cOOkie", "xx=yyy")

	if string(h.Cookie("foo")) != "bar" {
		t.Fatalf("Unexpected cookie %q. Expecting %q", h.Cookie("foo"), "bar")
	}
	if string(h.Cookie("baz")) != "aaa" {
		t.Fatalf("Unexpected cookie %q. Expecting %q", h.Cookie("baz"), "aaa")
	}
	if string(h.Cookie("xx")) != "yyy" {
		t.Fatalf("unexpected cookie %q. Expecting %q", h.Cookie("xx"), "yyy")
	}
}

func TestResponseHeaderSetCookie(t *testing.T) {
	t.Parallel()

	var h ResponseHeader

	h.Set("set-cookie", "foo=bar; path=/aa/bb; domain=aaa.com")
	h.Set(HeaderSetCookie, "aaaaa=bxx")

	var c Cookie
	c.SetKey("foo")
	if !h.Cookie(&c) {
		t.Fatalf("cannot obtain %q cookie", c.Key())
	}
	if string(c.Value()) != "bar" {
		t.Fatalf("unexpected cookie value %q. Expected %q", c.Value(), "bar")
	}
	if string(c.Path()) != "/aa/bb" {
		t.Fatalf("unexpected cookie path %q. Expected %q", c.Path(), "/aa/bb")
	}
	if string(c.Domain()) != "aaa.com" {
		t.Fatalf("unexpected cookie domain %q. Expected %q", c.Domain(), "aaa.com")
	}

	c.SetKey("aaaaa")
	if !h.Cookie(&c) {
		t.Fatalf("cannot obtain %q cookie", c.Key())
	}
	if string(c.Value()) != "bxx" {
		t.Fatalf("unexpected cookie value %q. Expecting %q", c.Value(), "bxx")
	}
}

func TestResponseHeaderVisitAll(t *testing.T) {
	t.Parallel()

	var h ResponseHeader

	r := bytes.NewBufferString("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Encoding: gzip\r\nContent-Length: 123\r\nSet-Cookie: aa=bb; path=/foo/bar\r\nSet-Cookie: ccc\r\nTrailer: Foo, Bar\r\n\r\n")
	br := bufio.NewReader(r)
	if err := h.Read(br); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if h.Len() != 6 {
		t.Fatalf("Unexpected number of headers: %d. Expected 6", h.Len())
	}
	contentLengthCount := 0
	contentTypeCount := 0
	contentEncodingCount := 0
	cookieCount := 0
	h.VisitAll(func(key, value []byte) {
		k := string(key)
		v := string(value)
		switch k {
		case HeaderContentLength:
			if v != string(h.Peek(k)) {
				t.Fatalf("unexpected content-length: %q. Expecting %q", v, h.Peek(k))
			}
			contentLengthCount++
		case HeaderContentType:
			if v != string(h.Peek(k)) {
				t.Fatalf("Unexpected content-type: %q. Expected %q", v, h.Peek(k))
			}
			contentTypeCount++
		case HeaderContentEncoding:
			if v != string(h.Peek(k)) {
				t.Fatalf("Unexpected content-encoding: %q. Expected %q", v, h.Peek(k))
			}
			contentEncodingCount++
		case HeaderSetCookie:
			if cookieCount == 0 && v != "aa=bb; path=/foo/bar" {
				t.Fatalf("unexpected cookie header: %q. Expected %q", v, "aa=bb; path=/foo/bar")
			}
			if cookieCount == 1 && v != "ccc" {
				t.Fatalf("unexpected cookie header: %q. Expected %q", v, "ccc")
			}
			cookieCount++
		case HeaderTrailer:
			if v != "Foo, Bar" {
				t.Fatalf("Unexpected trailer header %q. Expected %q", v, "Foo, Bar")
			}
		default:
			t.Fatalf("unexpected header %q=%q", k, v)
		}
	})
	if contentLengthCount != 1 {
		t.Fatalf("unexpected number of content-length headers: %d. Expected 1", contentLengthCount)
	}
	if contentTypeCount != 1 {
		t.Fatalf("unexpected number of content-type headers: %d. Expected 1", contentTypeCount)
	}
	if contentEncodingCount != 1 {
		t.Fatalf("unexpected number of content-encoding headers: %d. Expected 1", contentEncodingCount)
	}
	if cookieCount != 2 {
		t.Fatalf("unexpected number of cookie header: %d. Expected 2", cookieCount)
	}
}

func TestRequestHeaderVisitAll(t *testing.T) {
	t.Parallel()

	var h RequestHeader

	r := bytes.NewBufferString("GET / HTTP/1.1\r\nHost: aa.com\r\nXX: YYY\r\nXX: ZZ\r\nCookie: a=b; c=d\r\nTrailer: Foo, Bar\r\n\r\n")
	br := bufio.NewReader(r)
	if err := h.Read(br); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if h.Len() != 5 {
		t.Fatalf("Unexpected number of header: %d. Expected 5", h.Len())
	}
	hostCount := 0
	xxCount := 0
	cookieCount := 0
	h.VisitAll(func(key, value []byte) {
		k := string(key)
		v := string(value)
		switch k {
		case HeaderHost:
			if v != string(h.Peek(k)) {
				t.Fatalf("Unexpected host value %q. Expected %q", v, h.Peek(k))
			}
			hostCount++
		case "Xx":
			if xxCount == 0 && v != "YYY" {
				t.Fatalf("Unexpected value %q. Expected %q", v, "YYY")
			}
			if xxCount == 1 && v != "ZZ" {
				t.Fatalf("Unexpected value %q. Expected %q", v, "ZZ")
			}
			xxCount++
		case HeaderCookie:
			if v != "a=b; c=d" {
				t.Fatalf("Unexpected cookie %q. Expected %q", v, "a=b; c=d")
			}
			cookieCount++
		case HeaderTrailer:
			if v != "Foo, Bar" {
				t.Fatalf("Unexpected trailer header %q. Expected %q", v, "Foo, Bar")
			}
		default:
			t.Fatalf("Unexpected header %q=%q", k, v)
		}
	})
	if hostCount != 1 {
		t.Fatalf("Unexpected number of host headers detected %d. Expected 1", hostCount)
	}
	if xxCount != 2 {
		t.Fatalf("Unexpected number of xx headers detected %d. Expected 2", xxCount)
	}
	if cookieCount != 1 {
		t.Fatalf("Unexpected number of cookie headers %d. Expected 1", cookieCount)
	}
}

func TestRequestHeaderVisitAllInOrder(t *testing.T) {
	t.Parallel()

	var h RequestHeader

	r := bytes.NewBufferString("GET / HTTP/1.1\r\nContent-Type: aa\r\nCookie: a=b\r\nHost: example.com\r\nUser-Agent: xxx\r\n\r\n")
	br := bufio.NewReader(r)
	if err := h.Read(br); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if h.Len() != 4 {
		t.Fatalf("Unexpected number of headers: %d. Expected 4", h.Len())
	}

	order := []string{
		HeaderContentType,
		HeaderCookie,
		HeaderHost,
		HeaderUserAgent,
	}
	values := []string{
		"aa",
		"a=b",
		"example.com",
		"xxx",
	}

	h.VisitAllInOrder(func(key, value []byte) {
		if len(order) == 0 {
			t.Fatalf("no more headers expected, got %q", key)
		}
		if order[0] != string(key) {
			t.Fatalf("expected header %q got %q", order[0], key)
		}
		if values[0] != string(value) {
			t.Fatalf("expected header value %q got %q", values[0], value)
		}
		order = order[1:]
		values = values[1:]
	})
}

func TestResponseHeaderAddTrailerError(t *testing.T) {
	t.Parallel()

	var h ResponseHeader
	err := h.AddTrailer("Foo,   Content-Length , bAr,Transfer-Encoding, uSer aGent")
	expectedTrailer := "Foo, Bar, uSer aGent"

	if !errors.Is(err, ErrBadTrailer) {
		t.Fatalf("unexpected err %q. Expected %q", err, ErrBadTrailer)
	}
	if trailer := string(h.Peek(HeaderTrailer)); trailer != expectedTrailer {
		t.Fatalf("unexpected trailer %q. Expected %q", trailer, expectedTrailer)
	}
}

func TestRequestHeaderAddTrailerError(t *testing.T) {
	t.Parallel()

	var h RequestHeader
	err := h.AddTrailer("Foo,   Content-Length , Bar,Transfer-Encoding,")
	expectedTrailer := "Foo, Bar"

	if !errors.Is(err, ErrBadTrailer) {
		t.Fatalf("unexpected err %q. Expected %q", err, ErrBadTrailer)
	}
	if trailer := string(h.Peek(HeaderTrailer)); trailer != expectedTrailer {
		t.Fatalf("unexpected trailer %q. Expected %q", trailer, expectedTrailer)
	}
}

// Security tests for trailer handling vulnerability fix.
func TestTrailerSecurityVulnerabilityFix(t *testing.T) {
	t.Parallel()

	// Test cases for headers that should be blocked in trailers
	dangerousHeaders := []struct {
		name        string
		header      string
		description string
	}{
		{"Content-Type", "Content-Type", "off-by-one fix: exactly 'Content-Type' should be blocked"},
		{"Cookie", "Cookie", "session hijacking prevention"},
		{"Set-Cookie", "Set-Cookie", "session hijacking prevention"},
		{"Location", "Location", "redirect attack prevention"},
		{"X-Forwarded-For", "X-Forwarded-For", "IP spoofing prevention"},
		{"X-Forwarded-Host", "X-Forwarded-Host", "IP spoofing prevention"},
		{"X-Forwarded-Proto", "X-Forwarded-Proto", "IP spoofing prevention"},
		{"X-Real-IP", "X-Real-IP", "IP spoofing prevention"},
		{"X-Real-Ip", "X-Real-Ip", "IP spoofing prevention (case insensitive)"},
		{"Authorization", "Authorization", "auth bypass prevention"},
		{"Host", "Host", "host header attack prevention"},
		{"Connection", "Connection", "connection control prevention"},
	}

	// Test RequestHeader AddTrailer blocking dangerous headers.
	for _, tc := range dangerousHeaders {
		t.Run("RequestHeader_"+tc.name, func(t *testing.T) {
			var h RequestHeader
			err := h.AddTrailer(tc.header)
			if !errors.Is(err, ErrBadTrailer) {
				t.Fatalf("Expected ErrBadTrailer for %s (%s), got: %v", tc.header, tc.description, err)
			}

			// Verify trailer header is empty since the dangerous header was rejected
			if trailer := string(h.Peek(HeaderTrailer)); trailer != "" {
				t.Fatalf("Expected empty trailer after rejecting %s, got: %q", tc.header, trailer)
			}
		})
	}

	// Test ResponseHeader AddTrailer blocking dangerous headers
	for _, tc := range dangerousHeaders {
		t.Run("ResponseHeader_"+tc.name, func(t *testing.T) {
			var h ResponseHeader
			err := h.AddTrailer(tc.header)

			if !errors.Is(err, ErrBadTrailer) {
				t.Fatalf("Expected ErrBadTrailer for %s (%s), got: %v", tc.header, tc.description, err)
			}

			// Verify trailer header is empty since the dangerous header was rejected
			if trailer := string(h.Peek(HeaderTrailer)); trailer != "" {
				t.Fatalf("Expected empty trailer after rejecting %s, got: %q", tc.header, trailer)
			}
		})
	}

	// Test that safe headers are still allowed
	safeHeaders := []string{"Foo", "X-Custom-Safe", "My-App-Trailer", "Debug-Info"}

	for _, header := range safeHeaders {
		t.Run("Safe_RequestHeader_"+header, func(t *testing.T) {
			var h RequestHeader
			err := h.AddTrailer(header)
			if err != nil {
				t.Fatalf("Expected no error for safe header %s, got: %v", header, err)
			}

			// Verify the safe header was added to trailer
			if trailer := string(h.Peek(HeaderTrailer)); trailer != header {
				t.Fatalf("Expected trailer %q for safe header, got: %q", header, trailer)
			}
		})

		t.Run("Safe_ResponseHeader_"+header, func(t *testing.T) {
			var h ResponseHeader
			err := h.AddTrailer(header)
			if err != nil {
				t.Fatalf("Expected no error for safe header %s, got: %v", header, err)
			}

			// Verify the safe header was added to trailer
			if trailer := string(h.Peek(HeaderTrailer)); trailer != header {
				t.Fatalf("Expected trailer %q for safe header, got: %q", header, trailer)
			}
		})
	}
}

func TestTrailerParsingSecurityFix(t *testing.T) {
	t.Parallel()

	// Test the specific vulnerability scenario: malicious trailers should be rejected
	// Test that dangerous trailers in chunked body are properly blocked

	dangerousTrailers := []string{
		"Content-Type: text/malicious\r\n\r\n",
		"X-Forwarded-For: attacker.com\r\n\r\n",
		"X-Real-IP: 1.1.1.1\r\n\r\n",
		"Cookie: evil\r\n\r\n",
		"Location: http://evil.com\r\n\r\n",
	}

	for i, trailer := range dangerousTrailers {
		t.Run("Request_"+strconv.Itoa(i), func(t *testing.T) {
			var h RequestHeader
			r := bytes.NewBufferString(trailer)
			br := bufio.NewReader(r)

			err := h.ReadTrailer(br)
			if err == nil {
				t.Fatalf("Expected error when reading dangerous trailer, but got none: %s", trailer)
			}

			// The error should mention forbidden trailer
			if !strings.Contains(err.Error(), "forbidden trailer") {
				t.Fatalf("Expected 'forbidden trailer' error for %s, got: %v", trailer, err)
			}
		})

		t.Run("Response_"+strconv.Itoa(i), func(t *testing.T) {
			var h ResponseHeader
			r := bytes.NewBufferString(trailer)
			br := bufio.NewReader(r)

			err := h.ReadTrailer(br)
			if err == nil {
				t.Fatalf("Expected error when reading dangerous trailer, but got none: %s", trailer)
			}

			// The error should mention forbidden trailer
			if !strings.Contains(err.Error(), "forbidden trailer") {
				t.Fatalf("Expected 'forbidden trailer' error for %s, got: %v", trailer, err)
			}
		})
	}

	// Test that safe trailers still work
	safeTrailers := []string{
		"Foo: bar\r\n\r\n",
		"X-Custom-Header: value\r\n\r\n",
		"Debug-Info: test\r\n\r\n",
	}

	for i, trailer := range safeTrailers {
		t.Run("Safe_Request_"+strconv.Itoa(i), func(t *testing.T) {
			var h RequestHeader
			r := bytes.NewBufferString(trailer)
			br := bufio.NewReader(r)

			err := h.ReadTrailer(br)
			if err != nil && err != io.EOF {
				t.Fatalf("Expected no error for safe trailer %s, got: %v", trailer, err)
			}
		})

		t.Run("Safe_Response_"+strconv.Itoa(i), func(t *testing.T) {
			var h ResponseHeader
			r := bytes.NewBufferString(trailer)
			br := bufio.NewReader(r)

			err := h.ReadTrailer(br)
			if err != nil && err != io.EOF {
				t.Fatalf("Expected no error for safe trailer %s, got: %v", trailer, err)
			}
		})
	}
}

func TestResponseHeaderCookie(t *testing.T) {
	t.Parallel()

	var h ResponseHeader
	var c Cookie

	c.SetKey("foobar")
	c.SetValue("aaa")
	h.SetCookie(&c)

	c.SetKey("йцук")
	c.SetDomain("foobar.com")
	h.SetCookie(&c)

	c.Reset()
	c.SetKey("foobar")
	if !h.Cookie(&c) {
		t.Fatalf("Cannot find cookie %q", c.Key())
	}

	var expectedC1 Cookie
	expectedC1.SetKey("foobar")
	expectedC1.SetValue("aaa")
	if !equalCookie(&expectedC1, &c) {
		t.Fatalf("unexpected cookie\n%#v\nExpected\n%#v\n", &c, &expectedC1)
	}

	c.SetKey("йцук")
	if !h.Cookie(&c) {
		t.Fatalf("cannot find cookie %q", c.Key())
	}

	var expectedC2 Cookie
	expectedC2.SetKey("йцук")
	expectedC2.SetValue("aaa")
	expectedC2.SetDomain("foobar.com")
	if !equalCookie(&expectedC2, &c) {
		t.Fatalf("unexpected cookie\n%v\nExpected\n%v\n", &c, &expectedC2)
	}

	for key, value := range h.Cookies() {
		var cc Cookie
		if err := cc.ParseBytes(value); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(key, cc.Key()) {
			t.Fatalf("Unexpected cookie key %q. Expected %q", key, cc.Key())
		}
		switch {
		case bytes.Equal(key, []byte("foobar")):
			if !equalCookie(&expectedC1, &cc) {
				t.Fatalf("unexpected cookie\n%v\nExpected\n%v\n", &cc, &expectedC1)
			}
		case bytes.Equal(key, []byte("йцук")):
			if !equalCookie(&expectedC2, &cc) {
				t.Fatalf("unexpected cookie\n%v\nExpected\n%v\n", &cc, &expectedC2)
			}
		default:
			t.Fatalf("unexpected cookie key %q", key)
		}
	}

	w := &bytes.Buffer{}
	bw := bufio.NewWriter(w)
	if err := h.Write(bw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	h.DelAllCookies()

	var h1 ResponseHeader
	br := bufio.NewReader(w)
	if err := h1.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c.SetKey("foobar")
	if !h1.Cookie(&c) {
		t.Fatalf("Cannot find cookie %q", c.Key())
	}
	if !equalCookie(&expectedC1, &c) {
		t.Fatalf("unexpected cookie\n%v\nExpected\n%v\n", &c, &expectedC1)
	}

	h1.DelCookie("foobar")
	if h.Cookie(&c) {
		t.Fatalf("Unexpected cookie found: %v", &c)
	}
	if h1.Cookie(&c) {
		t.Fatalf("Unexpected cookie found: %v", &c)
	}

	c.SetKey("йцук")
	if !h1.Cookie(&c) {
		t.Fatalf("cannot find cookie %q", c.Key())
	}
	if !equalCookie(&expectedC2, &c) {
		t.Fatalf("unexpected cookie\n%v\nExpected\n%v\n", &c, &expectedC2)
	}

	h1.DelCookie("йцук")
	if h.Cookie(&c) {
		t.Fatalf("Unexpected cookie found: %v", &c)
	}
	if h1.Cookie(&c) {
		t.Fatalf("Unexpected cookie found: %v", &c)
	}
}

func equalCookie(c1, c2 *Cookie) bool {
	if !bytes.Equal(c1.Key(), c2.Key()) {
		return false
	}
	if !bytes.Equal(c1.Value(), c2.Value()) {
		return false
	}
	if !c1.Expire().Equal(c2.Expire()) {
		return false
	}
	if !bytes.Equal(c1.Domain(), c2.Domain()) {
		return false
	}
	if !bytes.Equal(c1.Path(), c2.Path()) {
		return false
	}
	return true
}

func TestRequestHeaderCookie(t *testing.T) {
	t.Parallel()

	var h RequestHeader
	h.SetRequestURI("/foobar")
	h.Set(HeaderHost, "foobar.com")

	h.SetCookie("foo", "bar")
	h.SetCookie("привет", "мир")

	if string(h.Cookie("foo")) != "bar" {
		t.Fatalf("Unexpected cookie value %q. Expected %q", h.Cookie("foo"), "bar")
	}
	if string(h.Cookie("привет")) != "мир" {
		t.Fatalf("Unexpected cookie value %q. Expected %q", h.Cookie("привет"), "мир")
	}

	w := &bytes.Buffer{}
	bw := bufio.NewWriter(w)
	if err := h.Write(bw); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	var h1 RequestHeader
	br := bufio.NewReader(w)
	if err := h1.Read(br); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !bytes.Equal(h1.Cookie("foo"), h.Cookie("foo")) {
		t.Fatalf("Unexpected cookie value %q. Expected %q", h1.Cookie("foo"), h.Cookie("foo"))
	}
	h1.DelCookie("foo")
	if len(h1.Cookie("foo")) > 0 {
		t.Fatalf("Unexpected cookie found: %q", h1.Cookie("foo"))
	}
	if !bytes.Equal(h1.Cookie("привет"), h.Cookie("привет")) {
		t.Fatalf("Unexpected cookie value %q. Expected %q", h1.Cookie("привет"), h.Cookie("привет"))
	}
	h1.DelCookie("привет")
	if len(h1.Cookie("привет")) > 0 {
		t.Fatalf("Unexpected cookie found: %q", h1.Cookie("привет"))
	}

	h.DelAllCookies()
	if len(h.Cookie("foo")) > 0 {
		t.Fatalf("Unexpected cookie found: %q", h.Cookie("foo"))
	}
	if len(h.Cookie("привет")) > 0 {
		t.Fatalf("Unexpected cookie found: %q", h.Cookie("привет"))
	}
}

func TestResponseHeaderCookieIssue4(t *testing.T) {
	t.Parallel()

	var h ResponseHeader

	c := AcquireCookie()
	c.SetKey("foo")
	c.SetValue("bar")
	h.SetCookie(c)

	if string(h.Peek(HeaderSetCookie)) != "foo=bar" {
		t.Fatalf("Unexpected Set-Cookie header %q. Expected %q", h.Peek(HeaderSetCookie), "foo=bar")
	}
	cookieSeen := false
	for key := range h.All() {
		if string(key) == HeaderSetCookie {
			cookieSeen = true
			break
		}
	}
	if !cookieSeen {
		t.Fatalf("Set-Cookie not present in VisitAll")
	}

	c = AcquireCookie()
	c.SetKey("foo")
	h.Cookie(c)
	if string(c.Value()) != "bar" {
		t.Fatalf("Unexpected cookie value %q. Expected %q", c.Value(), "bar")
	}

	if string(h.Peek(HeaderSetCookie)) != "foo=bar" {
		t.Fatalf("Unexpected Set-Cookie header %q. Expected %q", h.Peek(HeaderSetCookie), "foo=bar")
	}
	cookieSeen = false
	for key := range h.All() {
		if string(key) == HeaderSetCookie {
			cookieSeen = true
			break
		}
	}
	if !cookieSeen {
		t.Fatalf("Set-Cookie not present in VisitAll")
	}
}

func TestRequestHeaderCookieIssue313(t *testing.T) {
	t.Parallel()

	var h RequestHeader
	h.SetRequestURI("/")
	h.Set(HeaderHost, "foobar.com")

	h.SetCookie("foo", "bar")

	if string(h.Peek(HeaderCookie)) != "foo=bar" {
		t.Fatalf("Unexpected Cookie header %q. Expected %q", h.Peek(HeaderCookie), "foo=bar")
	}
	cookieSeen := false
	for key := range h.All() {
		if string(key) == HeaderCookie {
			cookieSeen = true
			break
		}
	}
	if !cookieSeen {
		t.Fatalf("Cookie not present in VisitAll")
	}

	if string(h.Cookie("foo")) != "bar" {
		t.Fatalf("Unexpected cookie value %q. Expected %q", h.Cookie("foo"), "bar")
	}

	if string(h.Peek(HeaderCookie)) != "foo=bar" {
		t.Fatalf("Unexpected Cookie header %q. Expected %q", h.Peek(HeaderCookie), "foo=bar")
	}
	cookieSeen = false
	for key := range h.All() {
		if string(key) == HeaderCookie {
			cookieSeen = true
			break
		}
	}
	if !cookieSeen {
		t.Fatalf("Cookie not present in VisitAll")
	}
}

func TestRequestHeaderMethod(t *testing.T) {
	t.Parallel()

	// common http methods
	testRequestHeaderMethod(t, MethodGet)
	testRequestHeaderMethod(t, MethodPost)
	testRequestHeaderMethod(t, MethodHead)
	testRequestHeaderMethod(t, MethodDelete)

	// non-http methods
	testRequestHeaderMethod(t, "foobar")
	testRequestHeaderMethod(t, "ABC")
}

func testRequestHeaderMethod(t *testing.T, expectedMethod string) {
	var h RequestHeader
	h.SetMethod(expectedMethod)
	m := h.Method()
	if string(m) != expectedMethod {
		t.Fatalf("unexpected method: %q. Expecting %q", m, expectedMethod)
	}

	s := h.String()
	var h1 RequestHeader
	br := bufio.NewReader(bytes.NewBufferString(s))
	if err := h1.Read(br); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m1 := h1.Method()
	if !bytes.Equal(m, m1) {
		t.Fatalf("unexpected method: %q. Expecting %q", m, m1)
	}
}

func TestRequestHeaderSetGet(t *testing.T) {
	t.Parallel()

	h := &RequestHeader{}
	h.SetRequestURI("/aa/bbb")
	h.SetMethod(MethodPost)
	h.Set("foo", "bar")
	h.Set("host", "12345")
	h.Set("content-type", "aaa/bbb")
	h.Set("content-length", "1234")
	h.Set("user-agent", "aaabbb")
	h.Set("referer", "axcv")
	h.Set("baz", "xxxxx")
	h.Set("transfer-encoding", "chunked")
	h.Set("connection", "close")

	expectRequestHeaderGet(t, h, "Foo", "bar")
	expectRequestHeaderGet(t, h, HeaderHost, "12345")
	expectRequestHeaderGet(t, h, HeaderContentType, "aaa/bbb")
	expectRequestHeaderGet(t, h, HeaderContentLength, "1234")
	expectRequestHeaderGet(t, h, "USER-AGent", "aaabbb")
	expectRequestHeaderGet(t, h, HeaderReferer, "axcv")
	expectRequestHeaderGet(t, h, "baz", "xxxxx")
	expectRequestHeaderGet(t, h, HeaderTransferEncoding, "")
	expectRequestHeaderGet(t, h, "connecTION", "close")
	if !h.ConnectionClose() {
		t.Fatalf("unset connection: close")
	}

	if h.ContentLength() != 1234 {
		t.Fatalf("Unexpected content-length %d. Expected %d", h.ContentLength(), 1234)
	}

	w := &bytes.Buffer{}
	bw := bufio.NewWriter(w)
	err := h.Write(bw)
	if err != nil {
		t.Fatalf("Unexpected error when writing request header: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("Unexpected error when flushing request header: %v", err)
	}

	var h1 RequestHeader
	br := bufio.NewReader(w)
	if err = h1.Read(br); err != nil {
		t.Fatalf("Unexpected error when reading request header: %v", err)
	}

	if h1.ContentLength() != h.ContentLength() {
		t.Fatalf("Unexpected Content-Length %d. Expected %d", h1.ContentLength(), h.ContentLength())
	}

	expectRequestHeaderGet(t, &h1, "Foo", "bar")
	expectRequestHeaderGet(t, &h1, "HOST", "12345")
	expectRequestHeaderGet(t, &h1, HeaderContentType, "aaa/bbb")
	expectRequestHeaderGet(t, &h1, HeaderContentLength, "1234")
	expectRequestHeaderGet(t, &h1, "USER-AGent", "aaabbb")
	expectRequestHeaderGet(t, &h1, HeaderReferer, "axcv")
	expectRequestHeaderGet(t, &h1, "baz", "xxxxx")
	expectRequestHeaderGet(t, &h1, HeaderTransferEncoding, "")
	expectRequestHeaderGet(t, &h1, HeaderConnection, "close")
	if !h1.ConnectionClose() {
		t.Fatalf("unset connection: close")
	}
}

func TestResponseHeaderSetGet(t *testing.T) {
	t.Parallel()

	h := &ResponseHeader{}
	h.Set("foo", "bar")
	h.Set("content-type", "aaa/bbb")
	h.Set("content-encoding", "gzip")
	h.Set("connection", "close")
	h.Set("content-length", "1234")
	h.Set(HeaderServer, "aaaa")
	h.Set("baz", "xxxxx")
	h.Set(HeaderTransferEncoding, "chunked")

	expectResponseHeaderGet(t, h, "Foo", "bar")
	expectResponseHeaderGet(t, h, HeaderContentType, "aaa/bbb")
	expectResponseHeaderGet(t, h, HeaderContentEncoding, "gzip")
	expectResponseHeaderGet(t, h, HeaderConnection, "close")
	expectResponseHeaderGet(t, h, HeaderContentLength, "1234")
	expectResponseHeaderGet(t, h, "seRVer", "aaaa")
	expectResponseHeaderGet(t, h, "baz", "xxxxx")
	expectResponseHeaderGet(t, h, HeaderTransferEncoding, "")

	if h.ContentLength() != 1234 {
		t.Fatalf("Unexpected content-length %d. Expected %d", h.ContentLength(), 1234)
	}
	if !h.ConnectionClose() {
		t.Fatalf("Unexpected Connection: close value %v. Expected %v", h.ConnectionClose(), true)
	}

	w := &bytes.Buffer{}
	bw := bufio.NewWriter(w)
	err := h.Write(bw)
	if err != nil {
		t.Fatalf("Unexpected error when writing response header: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("Unexpected error when flushing response header: %v", err)
	}

	var h1 ResponseHeader
	br := bufio.NewReader(w)
	if err = h1.Read(br); err != nil {
		t.Fatalf("Unexpected error when reading response header: %v", err)
	}

	if h1.ContentLength() != h.ContentLength() {
		t.Fatalf("Unexpected Content-Length %d. Expected %d", h1.ContentLength(), h.ContentLength())
	}
	if h1.ConnectionClose() != h.ConnectionClose() {
		t.Fatalf("unexpected connection: close %v. Expected %v", h1.ConnectionClose(), h.ConnectionClose())
	}

	expectResponseHeaderGet(t, &h1, "Foo", "bar")
	expectResponseHeaderGet(t, &h1, HeaderContentType, "aaa/bbb")
	expectResponseHeaderGet(t, &h1, HeaderContentEncoding, "gzip")
	expectResponseHeaderGet(t, &h1, HeaderConnection, "close")
	expectResponseHeaderGet(t, &h1, "seRVer", "aaaa")
	expectResponseHeaderGet(t, &h1, "baz", "xxxxx")
}

func expectRequestHeaderGet(t *testing.T, h *RequestHeader, key, expectedValue string) {
	if string(h.Peek(key)) != expectedValue {
		t.Fatalf("Unexpected value for key %q: %q. Expected %q", key, h.Peek(key), expectedValue)
	}
}

func expectResponseHeaderGet(t *testing.T, h *ResponseHeader, key, expectedValue string) {
	if string(h.Peek(key)) != expectedValue {
		t.Fatalf("Unexpected value for key %q: %q. Expected %q", key, h.Peek(key), expectedValue)
	}
}

func TestResponseHeaderConnectionClose(t *testing.T) {
	t.Parallel()

	testResponseHeaderConnectionClose(t, true)
	testResponseHeaderConnectionClose(t, false)
}

func testResponseHeaderConnectionClose(t *testing.T, connectionClose bool) {
	h := &ResponseHeader{}
	if connectionClose {
		h.SetConnectionClose()
	}
	h.SetContentLength(123)

	w := &bytes.Buffer{}
	bw := bufio.NewWriter(w)
	err := h.Write(bw)
	if err != nil {
		t.Fatalf("Unexpected error when writing response header: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("Unexpected error when flushing response header: %v", err)
	}

	var h1 ResponseHeader
	br := bufio.NewReader(w)
	err = h1.Read(br)
	if err != nil {
		t.Fatalf("Unexpected error when reading response header: %v", err)
	}
	if h1.ConnectionClose() != h.ConnectionClose() {
		t.Fatalf("Unexpected value for ConnectionClose: %v. Expected %v", h1.ConnectionClose(), h.ConnectionClose())
	}
}

func TestRequestHeaderTooBig(t *testing.T) {
	t.Parallel()

	s := "GET / HTTP/1.1\r\nHost: aaa.com\r\n" + getHeaders(10500) + "\r\n"
	r := bytes.NewBufferString(s)
	br := bufio.NewReaderSize(r, 4096)
	h := &RequestHeader{}
	err := h.Read(br)
	if err == nil {
		t.Fatalf("Expecting error when reading too big header")
	}
}

func TestResponseHeaderTooBig(t *testing.T) {
	t.Parallel()

	s := "HTTP/1.1 200 OK\r\nContent-Type: sss\r\nContent-Length: 0\r\n" + getHeaders(100500) + "\r\n"
	r := bytes.NewBufferString(s)
	br := bufio.NewReaderSize(r, 4096)
	h := &ResponseHeader{}
	err := h.Read(br)
	if err == nil {
		t.Fatalf("Expecting error when reading too big header")
	}
}

type bufioPeekReader struct {
	s string
	n int
}

func (r *bufioPeekReader) Read(b []byte) (int, error) {
	if r.s == "" {
		return 0, io.EOF
	}

	r.n++
	n := r.n
	if len(r.s) < n {
		n = len(r.s)
	}
	src := []byte(r.s[:n])
	r.s = r.s[n:]
	n = copy(b, src)
	return n, nil
}

func TestRequestHeaderBufioPeek(t *testing.T) {
	t.Parallel()

	r := &bufioPeekReader{
		s: "GET / HTTP/1.1\r\nHost: foobar.com\r\n" + getHeaders(10) + "\r\naaaa",
	}
	br := bufio.NewReaderSize(r, 4096)
	h := &RequestHeader{}
	if err := h.Read(br); err != nil {
		t.Fatalf("Unexpected error when reading request: %v", err)
	}
	verifyRequestHeader(t, h, -2, "/", "foobar.com", "", "")
}

func TestResponseHeaderBufioPeek(t *testing.T) {
	t.Parallel()

	r := &bufioPeekReader{
		s: "HTTP/1.1 200 OK\r\nContent-Length: 10\r\nContent-Type: text/plain\r\nContent-Encoding: gzip\r\n" + getHeaders(10) + "\r\n0123456789",
	}
	br := bufio.NewReaderSize(r, 4096)
	h := &ResponseHeader{}
	if err := h.Read(br); err != nil {
		t.Fatalf("Unexpected error when reading response: %v", err)
	}
	verifyResponseHeader(t, h, 200, 10, "text/plain", "gzip")
}

func getHeaders(n int) string {
	var h []string
	for i := 0; i < n; i++ {
		h = append(h, fmt.Sprintf("Header_%d: Value_%d\r\n", i, i))
	}
	return strings.Join(h, "")
}

func TestResponseHeaderReadSuccess(t *testing.T) {
	t.Parallel()

	h := &ResponseHeader{}

	// straight order of content-length and content-type
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 200 OK\r\nContent-Length: 123\r\nContent-Type: text/html\r\n\r\n",
		200, 123, "text/html")
	if h.ConnectionClose() {
		t.Fatalf("unexpected connection: close")
	}

	// reverse order of content-length and content-type
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 202 OK\r\nContent-Type: text/plain; encoding=utf-8\r\nContent-Length: 543\r\nConnection: close\r\n\r\n",
		202, 543, "text/plain; encoding=utf-8")
	if !h.ConnectionClose() {
		t.Fatalf("expecting connection: close")
	}

	// transfer-encoding: chunked
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 505 Internal error\r\nContent-Type: text/html\r\nTransfer-Encoding: chunked\r\n\r\n",
		505, -1, "text/html")
	if h.ConnectionClose() {
		t.Fatalf("unexpected connection: close")
	}

	// reverse order of content-type and transfer-encoding
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 343 foobar\r\nTransfer-Encoding: chunked\r\nContent-Type: text/json\r\n\r\n",
		343, -1, "text/json")

	// additional headers
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 100 Continue\r\nFoobar: baz\r\nContent-Type: aaa/bbb\r\nUser-Agent: x\r\nContent-Length: 123\r\nZZZ: werer\r\n\r\n",
		100, 123, "aaa/bbb")

	// ancient http protocol
	testResponseHeaderReadSuccess(t, h, "HTTP/0.9 300 OK\r\nContent-Length: 123\r\nContent-Type: text/html\r\n\r\nqqqq",
		300, 123, "text/html")

	// lf instead of crlf
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 200 OK\nContent-Length: 123\nContent-Type: text/html\n\n",
		200, 123, "text/html")

	// No space after colon
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 200 OK\nContent-Length:34\nContent-Type: sss\n\naaaa",
		200, 34, "sss")

	// invalid case
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 400 OK\nconTEnt-leNGTH: 123\nConTENT-TYPE: ass\n\n",
		400, 123, "ass")

	// duplicate content-length
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 200 OK\r\nContent-Length: 456\r\nContent-Type: foo/bar\r\nContent-Length: 321\r\n\r\n",
		200, 321, "foo/bar")

	// duplicate content-type
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 200 OK\r\nContent-Length: 234\r\nContent-Type: foo/bar\r\nContent-Type: baz/bar\r\n\r\n",
		200, 234, "baz/bar")

	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 300 OK\r\nContent-Type: foo/barr\r\nTransfer-Encoding: chunked\r\nContent-Length: 354\r\n\r\n",
		300, -1, "foo/barr")

	// duplicate transfer-encoding: chunked
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n\r\n",
		200, -1, "text/html")

	// no reason string in the first line
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 456\r\nContent-Type: xxx/yyy\r\nContent-Length: 134\r\n\r\naaaxxx",
		456, 134, "xxx/yyy")

	// blank lines before the first line
	testResponseHeaderReadSuccess(t, h, "\r\nHTTP/1.1 200 OK\r\nContent-Type: aa\r\nContent-Length: 0\r\n\r\nsss",
		200, 0, "aa")
	if h.ConnectionClose() {
		t.Fatalf("unexpected connection: close")
	}

	// no content-length (informational responses)
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 101 OK\r\n\r\n",
		101, -2, "text/plain; charset=utf-8")
	if h.ConnectionClose() {
		t.Fatalf("expecting connection: keep-alive for informational response")
	}

	// no content-length (no-content responses)
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 204 OK\r\n\r\n",
		204, -2, "text/plain; charset=utf-8")
	if h.ConnectionClose() {
		t.Fatalf("expecting connection: keep-alive for no-content response")
	}

	// no content-length (not-modified responses)
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 304 OK\r\n\r\n",
		304, -2, "text/plain; charset=utf-8")
	if h.ConnectionClose() {
		t.Fatalf("expecting connection: keep-alive for not-modified response")
	}

	// no content-length (identity transfer-encoding)
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 200 OK\r\nContent-Type: foo/bar\r\n\r\nabcdefg",
		200, -2, "foo/bar")
	if !h.ConnectionClose() {
		t.Fatalf("expecting connection: close for identity response")
	}
	// See https://github.com/valyala/fasthttp/issues/1909
	if hasArg(h.h, HeaderTransferEncoding) {
		t.Fatalf("unexpected header: 'Transfer-Encoding' should not be present in parsed headers")
	}

	// no content-type
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 400 OK\r\nContent-Length: 123\r\n\r\nfoiaaa",
		400, 123, string(defaultContentType))

	// no content-type and no default
	h.SetNoDefaultContentType(true)
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 400 OK\r\nContent-Length: 123\r\n\r\nfoiaaa",
		400, 123, "")
	h.SetNoDefaultContentType(false)

	// no headers
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 200 OK\r\n\r\naaaabbb",
		200, -2, string(defaultContentType))
	if !h.IsHTTP11() {
		t.Fatalf("expecting http/1.1 protocol")
	}

	// ancient http protocol
	testResponseHeaderReadSuccess(t, h, "HTTP/1.0 203 OK\r\nContent-Length: 123\r\nContent-Type: foobar\r\n\r\naaa",
		203, 123, "foobar")
	if h.IsHTTP11() {
		t.Fatalf("ancient protocol must be non-http/1.1")
	}
	if !h.ConnectionClose() {
		t.Fatalf("expecting connection: close for ancient protocol")
	}

	// ancient http protocol with 'Connection: keep-alive' header.
	testResponseHeaderReadSuccess(t, h, "HTTP/1.0 403 aa\r\nContent-Length: 0\r\nContent-Type: 2\r\nConnection: Keep-Alive\r\n\r\nww",
		403, 0, "2")
	if h.IsHTTP11() {
		t.Fatalf("ancient protocol must be non-http/1.1")
	}
	if h.ConnectionClose() {
		t.Fatalf("expecting connection: keep-alive for ancient protocol")
	}
}

func TestRequestHeaderReadSuccess(t *testing.T) {
	t.Parallel()

	h := &RequestHeader{}

	// simple headers
	testRequestHeaderReadSuccess(t, h, "GET /foo/bar HTTP/1.1\r\nHost: google.com\r\n\r\n",
		-2, "/foo/bar", "google.com", "", "")
	if h.ConnectionClose() {
		t.Fatalf("unexpected connection: close header")
	}

	// simple headers with body
	testRequestHeaderReadSuccess(t, h, "GET /a/bar HTTP/1.1\r\nHost: gole.com\r\nconneCTION: close\r\n\r\nfoobar",
		-2, "/a/bar", "gole.com", "", "")
	if !h.ConnectionClose() {
		t.Fatalf("connection: close unset")
	}

	// ancient http protocol
	testRequestHeaderReadSuccess(t, h, "GET /bar HTTP/1.0\r\nHost: gole\r\n\r\npppp",
		-2, "/bar", "gole", "", "")
	if h.IsHTTP11() {
		t.Fatalf("ancient http protocol cannot be http/1.1")
	}
	if !h.ConnectionClose() {
		t.Fatalf("expecting connectionClose for ancient http protocol")
	}

	// ancient http protocol with 'Connection: keep-alive' header
	testRequestHeaderReadSuccess(t, h, "GET /aa HTTP/1.0\r\nHost: bb\r\nConnection: keep-alive\r\n\r\nxxx",
		-2, "/aa", "bb", "", "")
	if h.IsHTTP11() {
		t.Fatalf("ancient http protocol cannot be http/1.1")
	}
	if h.ConnectionClose() {
		t.Fatalf("unexpected 'connection: close' for ancient http protocol")
	}

	// complex headers with body
	testRequestHeaderReadSuccess(t, h, "GET /aabar HTTP/1.1\r\nAAA: bbb\r\nHost: ole.com\r\nAA: bb\r\n\r\nzzz",
		-2, "/aabar", "ole.com", "", "")
	if !h.IsHTTP11() {
		t.Fatalf("expecting http/1.1 protocol")
	}
	if h.ConnectionClose() {
		t.Fatalf("unexpected connection: close")
	}

	// lf instead of crlf
	testRequestHeaderReadSuccess(t, h, "GET /foo/bar HTTP/1.1\nHost: google.com\n\n",
		-2, "/foo/bar", "google.com", "", "")

	// post method
	testRequestHeaderReadSuccess(t, h, "POST /aaa?bbb HTTP/1.1\r\nHost: foobar.com\r\nContent-Length: 1235\r\nContent-Type: aaa\r\n\r\nabcdef",
		1235, "/aaa?bbb", "foobar.com", "", "aaa")

	// no space after colon
	testRequestHeaderReadSuccess(t, h, "GET /a HTTP/1.1\nHost:aaaxd\n\nsdfds",
		-2, "/a", "aaaxd", "", "")

	// get with zero content-length
	testRequestHeaderReadSuccess(t, h, "GET /xxx HTTP/1.1\nHost: aaa.com\nContent-Length: 0\n\n",
		0, "/xxx", "aaa.com", "", "")

	// get with non-zero content-length
	testRequestHeaderReadSuccess(t, h, "GET /xxx HTTP/1.1\nHost: aaa.com\nContent-Length: 123\n\n",
		123, "/xxx", "aaa.com", "", "")

	// invalid case
	testRequestHeaderReadSuccess(t, h, "GET /aaa HTTP/1.1\nhoST: bbb.com\n\naas",
		-2, "/aaa", "bbb.com", "", "")

	// referer
	testRequestHeaderReadSuccess(t, h, "GET /asdf HTTP/1.1\nHost: aaa.com\nReferer: bb.com\n\naaa",
		-2, "/asdf", "aaa.com", "bb.com", "")

	// duplicate host
	testRequestHeaderReadSuccess(t, h, "GET /aa HTTP/1.1\r\nHost: aaaaaa.com\r\nHost: bb.com\r\n\r\n",
		-2, "/aa", "bb.com", "", "")

	// post with duplicate content-type
	testRequestHeaderReadSuccess(t, h, "POST /a HTTP/1.1\r\nHost: aa\r\nContent-Type: ab\r\nContent-Length: 123\r\nContent-Type: xx\r\n\r\n",
		123, "/a", "aa", "", "xx")

	// non-post with content-type
	testRequestHeaderReadSuccess(t, h, "GET /aaa HTTP/1.1\r\nHost: bbb.com\r\nContent-Type: aaab\r\n\r\n",
		-2, "/aaa", "bbb.com", "", "aaab")

	// non-post with content-length
	testRequestHeaderReadSuccess(t, h, "HEAD / HTTP/1.1\r\nHost: aaa.com\r\nContent-Length: 123\r\n\r\n",
		123, "/", "aaa.com", "", "")

	// non-post with content-type and content-length
	testRequestHeaderReadSuccess(t, h, "GET /aa HTTP/1.1\r\nHost: aa.com\r\nContent-Type: abd/test\r\nContent-Length: 123\r\n\r\n",
		123, "/aa", "aa.com", "", "abd/test")

	// request uri with hostname
	testRequestHeaderReadSuccess(t, h, "GET http://gooGle.com/foO/%20bar?xxx#aaa HTTP/1.1\r\nHost: aa.cOM\r\n\r\ntrail",
		-2, "http://gooGle.com/foO/%20bar?xxx#aaa", "aa.cOM", "", "")

	// blank lines before the first line
	testRequestHeaderReadSuccess(t, h, "\r\n\n\r\nGET /aaa HTTP/1.1\r\nHost: aaa.com\r\n\r\nsss",
		-2, "/aaa", "aaa.com", "", "")

	// request uri with spaces
	testRequestHeaderReadSuccess(t, h, "GET /foo/ bar baz HTTP/1.1\r\nHost: aa.com\r\n\r\nxxx",
		-2, "/foo/ bar baz", "aa.com", "", "")

	// no host
	testRequestHeaderReadSuccess(t, h, "GET /foo/bar HTTP/1.1\r\nFOObar: assdfd\r\n\r\naaa",
		-2, "/foo/bar", "", "", "")

	// no host, no headers
	testRequestHeaderReadSuccess(t, h, "GET /foo/bar HTTP/1.1\r\n\r\nfoobar",
		-2, "/foo/bar", "", "", "")

	// post without content-length and content-type
	testRequestHeaderReadSuccess(t, h, "POST /aaa HTTP/1.1\r\nHost: aaa.com\r\n\r\nzxc",
		-2, "/aaa", "aaa.com", "", "")

	// post without content-type
	testRequestHeaderReadSuccess(t, h, "POST /abc HTTP/1.1\r\nHost: aa.com\r\nContent-Length: 123\r\n\r\npoiuy",
		123, "/abc", "aa.com", "", "")

	// post without content-length
	testRequestHeaderReadSuccess(t, h, "POST /abc HTTP/1.1\r\nHost: aa.com\r\nContent-Type: adv\r\n\r\n123456",
		-2, "/abc", "aa.com", "", "adv")

	// put request
	testRequestHeaderReadSuccess(t, h, "PUT /faa HTTP/1.1\r\nHost: aaa.com\r\nContent-Length: 123\r\nContent-Type: aaa\r\n\r\nxwwere",
		123, "/faa", "aaa.com", "", "aaa")
}

func TestResponseHeaderReadError(t *testing.T) {
	t.Parallel()

	h := &ResponseHeader{}

	// incorrect first line
	testResponseHeaderReadError(t, h, "")
	testResponseHeaderReadError(t, h, "fo")
	testResponseHeaderReadError(t, h, "foobarbaz")
	testResponseHeaderReadError(t, h, "HTTP/1.1")
	testResponseHeaderReadError(t, h, "HTTP/1.1 ")
	testResponseHeaderReadError(t, h, "HTTP/1.1 s")

	// non-numeric status code
	testResponseHeaderReadError(t, h, "HTTP/1.1 foobar OK\r\nContent-Length: 123\r\nContent-Type: text/html\r\n\r\n")
	testResponseHeaderReadError(t, h, "HTTP/1.1 123foobar OK\r\nContent-Length: 123\r\nContent-Type: text/html\r\n\r\n")
	testResponseHeaderReadError(t, h, "HTTP/1.1 foobar344 OK\r\nContent-Length: 123\r\nContent-Type: text/html\r\n\r\n")

	// non-numeric content-length
	testResponseHeaderReadError(t, h, "HTTP/1.1 200 OK\r\nContent-Length: faaa\r\nContent-Type: text/html\r\n\r\nfoobar")
	testResponseHeaderReadError(t, h, "HTTP/1.1 201 OK\r\nContent-Length: 123aa\r\nContent-Type: text/ht\r\n\r\naaa")
	testResponseHeaderReadError(t, h, "HTTP/1.1 200 OK\r\nContent-Length: aa124\r\nContent-Type: html\r\n\r\nxx")

	// no headers
	testResponseHeaderReadError(t, h, "HTTP/1.1 200 OK\r\n")

	// no trailing crlf
	testResponseHeaderReadError(t, h, "HTTP/1.1 200 OK\r\nContent-Length: 123\r\nContent-Type: text/html\r\n")

	// forbidden trailer
	testResponseHeaderReadError(t, h, "HTTP/1.1 200 OK\r\nContent-Length: -1\r\nTrailer: Foo, Content-Length\r\n\r\n")

	// no protocol in the first line
	testResponseHeaderReadError(t, h, "GET /foo/bar\r\nHost: google.com\r\n\r\nisdD")

	// zero-length headers
	testResponseHeaderReadError(t, h, "HTTP/1.1 200 OK\r\n: zero-key\r\n\r\n")
}

func TestResponseHeaderReadErrorSecureLog(t *testing.T) {
	t.Parallel()

	h := &ResponseHeader{}
	h.secureErrorLogMessage = true

	// incorrect first line
	testResponseHeaderReadSecuredError(t, h, "fo")
	testResponseHeaderReadSecuredError(t, h, "foobarbaz")
	testResponseHeaderReadSecuredError(t, h, "HTTP/1.1")
	testResponseHeaderReadSecuredError(t, h, "HTTP/1.1 ")
	testResponseHeaderReadSecuredError(t, h, "HTTP/1.1 s")

	// non-numeric status code
	testResponseHeaderReadSecuredError(t, h, "HTTP/1.1 foobar OK\r\nContent-Length: 123\r\nContent-Type: text/html\r\n\r\n")
	testResponseHeaderReadSecuredError(t, h, "HTTP/1.1 123foobar OK\r\nContent-Length: 123\r\nContent-Type: text/html\r\n\r\n")
	testResponseHeaderReadSecuredError(t, h, "HTTP/1.1 foobar344 OK\r\nContent-Length: 123\r\nContent-Type: text/html\r\n\r\n")

	// no headers
	testResponseHeaderReadSecuredError(t, h, "HTTP/1.1 200 OK\r\n")

	// no trailing crlf
	testResponseHeaderReadSecuredError(t, h, "HTTP/1.1 200 OK\r\nContent-Length: 123\r\nContent-Type: text/html\r\n")
}

func TestRequestHeaderReadError(t *testing.T) {
	t.Parallel()

	h := &RequestHeader{}

	// incorrect first line
	testRequestHeaderReadError(t, h, "")
	testRequestHeaderReadError(t, h, "fo")
	testRequestHeaderReadError(t, h, "GET ")
	testRequestHeaderReadError(t, h, "GET / HTTP/1.1\r")

	// missing RequestURI
	testRequestHeaderReadError(t, h, "GET  HTTP/1.1\r\nHost: google.com\r\n\r\n")

	// post with invalid content-length
	testRequestHeaderReadError(t, h, "POST /a HTTP/1.1\r\nHost: bb\r\nContent-Type: aa\r\nContent-Length: dff\r\n\r\nqwerty")

	// forbidden trailer
	testRequestHeaderReadError(t, h, "POST /a HTTP/1.1\r\nContent-Length: -1\r\nTrailer: Foo, Content-Length\r\n\r\n")

	// post with duplicate content-length
	testRequestHeaderReadError(t, h, "POST /xx HTTP/1.1\r\nHost: aa\r\nContent-Type: s\r\nContent-Length: 13\r\nContent-Length: 1\r\n\r\n")

	// Zero-length header
	testRequestHeaderReadError(t, h, "GET /foo/bar HTTP/1.1\r\n: zero-key\r\n\r\n")

	// Invalid method
	testRequestHeaderReadError(t, h, "G(ET /foo/bar HTTP/1.1\r\n: zero-key\r\n\r\n")
}

func TestRequestHeaderReadSecuredError(t *testing.T) {
	t.Parallel()

	h := &RequestHeader{}
	h.secureErrorLogMessage = true

	// incorrect first line
	testRequestHeaderReadSecuredError(t, h, "fo")
	testRequestHeaderReadSecuredError(t, h, "GET ")
	testRequestHeaderReadSecuredError(t, h, "GET / HTTP/1.1\r")

	// missing RequestURI
	testRequestHeaderReadSecuredError(t, h, "GET  HTTP/1.1\r\nHost: google.com\r\n\r\n")

	// post with invalid content-length
	testRequestHeaderReadSecuredError(t, h, "POST /a HTTP/1.1\r\nHost: bb\r\nContent-Type: aa\r\nContent-Length: dff\r\n\r\nqwerty")
}

func testResponseHeaderReadError(t *testing.T, h *ResponseHeader, headers string) {
	r := bytes.NewBufferString(headers)
	br := bufio.NewReader(r)
	err := h.Read(br)
	if err == nil {
		t.Fatalf("Expecting error when reading response header %q", headers)
	}
	// make sure response header works after error
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 200 OK\r\nContent-Type: foo/bar\r\nContent-Length: 12345\r\n\r\nsss",
		200, 12345, "foo/bar")
}

func testResponseHeaderReadSecuredError(t *testing.T, h *ResponseHeader, headers string) {
	r := bytes.NewBufferString(headers)
	br := bufio.NewReader(r)
	err := h.Read(br)
	if err == nil {
		t.Fatalf("Expecting error when reading response header %q", headers)
	}
	if strings.Contains(err.Error(), headers) {
		t.Fatalf("Not expecting header content in err %q", err)
	}
	// make sure response header works after error
	testResponseHeaderReadSuccess(t, h, "HTTP/1.1 200 OK\r\nContent-Type: foo/bar\r\nContent-Length: 12345\r\n\r\nsss",
		200, 12345, "foo/bar")
}

func testRequestHeaderReadError(t *testing.T, h *RequestHeader, headers string) {
	t.Helper()

	r := bytes.NewBufferString(headers)
	br := bufio.NewReader(r)
	err := h.Read(br)
	if err == nil {
		t.Fatalf("Expecting error when reading request header %q", headers)
	}

	// make sure request header works after error
	testRequestHeaderReadSuccess(t, h, "GET /foo/bar HTTP/1.1\r\nHost: aaaa\r\n\r\nxxx",
		-2, "/foo/bar", "aaaa", "", "")
}

func testRequestHeaderReadSecuredError(t *testing.T, h *RequestHeader, headers string) {
	r := bytes.NewBufferString(headers)
	br := bufio.NewReader(r)
	err := h.Read(br)
	if err == nil {
		t.Fatalf("Expecting error when reading request header %q", headers)
	}
	if strings.Contains(err.Error(), headers) {
		t.Fatalf("Not expecting header content in err %q", err)
	}
	// make sure request header works after error
	testRequestHeaderReadSuccess(t, h, "GET /foo/bar HTTP/1.1\r\nHost: aaaa\r\n\r\nxxx",
		-2, "/foo/bar", "aaaa", "", "")
}

func testResponseHeaderReadSuccess(t *testing.T, h *ResponseHeader, headers string, expectedStatusCode, expectedContentLength int,
	expectedContentType string,
) {
	t.Helper()

	r := bytes.NewBufferString(headers)
	br := bufio.NewReader(r)
	err := h.Read(br)
	if err != nil {
		t.Fatalf("Unexpected error when parsing response headers: %v. headers=%q", err, headers)
	}
	verifyResponseHeader(t, h, expectedStatusCode, expectedContentLength, expectedContentType, "")
}

func testRequestHeaderReadSuccess(t *testing.T, h *RequestHeader, headers string, expectedContentLength int,
	expectedRequestURI, expectedHost, expectedReferer, expectedContentType string,
) {
	t.Helper()

	r := bytes.NewBufferString(headers)
	br := bufio.NewReader(r)
	err := h.Read(br)
	if err != nil {
		t.Fatalf("Unexpected error when parsing request headers: %v. headers=%q", err, headers)
	}
	verifyRequestHeader(t, h, expectedContentLength, expectedRequestURI, expectedHost, expectedReferer, expectedContentType)
}

func verifyResponseHeader(t *testing.T, h *ResponseHeader, expectedStatusCode, expectedContentLength int, expectedContentType, expectedContentEncoding string) {
	if h.StatusCode() != expectedStatusCode {
		t.Fatalf("Unexpected status code %d. Expected %d", h.StatusCode(), expectedStatusCode)
	}
	if h.ContentLength() != expectedContentLength {
		t.Fatalf("Unexpected content length %d. Expected %d", h.ContentLength(), expectedContentLength)
	}
	if string(h.ContentType()) != expectedContentType {
		t.Fatalf("Unexpected content type %q. Expected %q", h.ContentType(), expectedContentType)
	}
	if string(h.ContentEncoding()) != expectedContentEncoding {
		t.Fatalf("Unexpected content encoding %q. Expected %q", h.ContentEncoding(), expectedContentEncoding)
	}
}

func verifyResponseHeaderConnection(t *testing.T, h *ResponseHeader, expectConnection string) {
	if string(h.Peek(HeaderConnection)) != expectConnection {
		t.Fatalf("Unexpected Connection %q. Expected %q", h.Peek(HeaderConnection), expectConnection)
	}
}

func verifyRequestHeader(t *testing.T, h *RequestHeader, expectedContentLength int,
	expectedRequestURI, expectedHost, expectedReferer, expectedContentType string,
) {
	if h.ContentLength() != expectedContentLength {
		t.Fatalf("Unexpected Content-Length %d. Expected %d", h.ContentLength(), expectedContentLength)
	}
	if string(h.RequestURI()) != expectedRequestURI {
		t.Fatalf("Unexpected RequestURI %q. Expected %q", h.RequestURI(), expectedRequestURI)
	}
	if string(h.Peek(HeaderHost)) != expectedHost {
		t.Fatalf("Unexpected host %q. Expected %q", h.Peek(HeaderHost), expectedHost)
	}
	if string(h.Peek(HeaderReferer)) != expectedReferer {
		t.Fatalf("Unexpected referer %q. Expected %q", h.Peek(HeaderReferer), expectedReferer)
	}
	if string(h.Peek(HeaderContentType)) != expectedContentType {
		t.Fatalf("Unexpected content-type %q. Expected %q", h.Peek(HeaderContentType), expectedContentType)
	}
}

func verifyResponseTrailer(t *testing.T, h *ResponseHeader, expectedTrailers map[string]string) {
	t.Helper()

	for k, v := range expectedTrailers {
		got := h.Peek(k)
		if !bytes.Equal(got, []byte(v)) {
			t.Fatalf("Unexpected trailer %q. Expected %q. Got %q", k, v, got)
		}
	}
}

func verifyRequestTrailer(t *testing.T, h *RequestHeader, expectedTrailers map[string]string) {
	for k, v := range expectedTrailers {
		got := h.Peek(k)
		if !bytes.Equal(got, []byte(v)) {
			t.Fatalf("Unexpected trailer %q. Expected %q. Got %q", k, v, got)
		}
	}
}

func verifyTrailer(t *testing.T, r *bufio.Reader, expectedTrailers map[string]string, isReq bool) {
	if isReq {
		req := Request{}
		err := req.Header.ReadTrailer(r)
		if err == io.EOF && expectedTrailers == nil {
			return
		}
		if err != nil {
			t.Fatalf("Cannot read trailer: %v", err)
		}
		verifyRequestTrailer(t, &req.Header, expectedTrailers)
		return
	}

	resp := Response{}
	err := resp.Header.ReadTrailer(r)
	if err == io.EOF && expectedTrailers == nil {
		return
	}
	if err != nil {
		t.Fatalf("Cannot read trailer: %v", err)
	}
	verifyResponseTrailer(t, &resp.Header, expectedTrailers)
}

func TestRequestHeader_PeekAll(t *testing.T) {
	t.Parallel()
	h := &RequestHeader{}
	h.Add(HeaderConnection, "keep-alive")
	h.Add("Content-Type", "aaa")
	h.Add(HeaderHost, "aaabbb")
	h.Add("User-Agent", "asdfas")
	h.Add("Content-Length", "1123")
	h.Add("Cookie", "foobar=baz")
	h.Add(HeaderTrailer, "foo, bar")
	h.Add("aaa", "aaa")
	h.Add("aaa", "bbb")

	expectRequestHeaderAll(t, h, HeaderConnection, [][]byte{s2b("keep-alive")})
	expectRequestHeaderAll(t, h, "Content-Type", [][]byte{s2b("aaa")})
	expectRequestHeaderAll(t, h, HeaderHost, [][]byte{s2b("aaabbb")})
	expectRequestHeaderAll(t, h, "User-Agent", [][]byte{s2b("asdfas")})
	expectRequestHeaderAll(t, h, "Content-Length", [][]byte{s2b("1123")})
	expectRequestHeaderAll(t, h, "Cookie", [][]byte{s2b("foobar=baz")})
	expectRequestHeaderAll(t, h, HeaderTrailer, [][]byte{s2b("Foo, Bar")})
	expectRequestHeaderAll(t, h, "aaa", [][]byte{s2b("aaa"), s2b("bbb")})

	h.Del("Content-Type")
	h.Del(HeaderHost)
	h.Del("aaa")
	expectRequestHeaderAll(t, h, "Content-Type", [][]byte{})
	expectRequestHeaderAll(t, h, HeaderHost, [][]byte{})
	expectRequestHeaderAll(t, h, "aaa", [][]byte{})
}

func expectRequestHeaderAll(t *testing.T, h *RequestHeader, key string, expectedValue [][]byte) {
	if len(h.PeekAll(key)) != len(expectedValue) {
		t.Fatalf("Unexpected size for key %q: %d. Expected %d", key, len(h.PeekAll(key)), len(expectedValue))
	}
	if !reflect.DeepEqual(h.PeekAll(key), expectedValue) {
		t.Fatalf("Unexpected value for key %q: %q. Expected %q", key, h.PeekAll(key), expectedValue)
	}
}

func TestResponseHeader_PeekAll(t *testing.T) {
	t.Parallel()

	h := &ResponseHeader{}
	h.Add(HeaderContentType, "aaa/bbb")
	h.Add(HeaderContentEncoding, "gzip")
	h.Add(HeaderConnection, "close")
	h.Add(HeaderContentLength, "1234")
	h.Add(HeaderServer, "aaaa")
	h.Add(HeaderSetCookie, "cccc")
	h.Add("aaa", "aaa")
	h.Add("aaa", "bbb")

	expectResponseHeaderAll(t, h, HeaderContentType, [][]byte{s2b("aaa/bbb")})
	expectResponseHeaderAll(t, h, HeaderContentEncoding, [][]byte{s2b("gzip")})
	expectResponseHeaderAll(t, h, HeaderConnection, [][]byte{s2b("close")})
	expectResponseHeaderAll(t, h, HeaderContentLength, [][]byte{s2b("1234")})
	expectResponseHeaderAll(t, h, HeaderServer, [][]byte{s2b("aaaa")})
	expectResponseHeaderAll(t, h, HeaderSetCookie, [][]byte{s2b("cccc")})
	expectResponseHeaderAll(t, h, "aaa", [][]byte{s2b("aaa"), s2b("bbb")})

	h.Del(HeaderContentType)
	h.Del(HeaderContentEncoding)
	expectResponseHeaderAll(t, h, HeaderContentType, [][]byte{defaultContentType})
	expectResponseHeaderAll(t, h, HeaderContentEncoding, [][]byte{})
}

func expectResponseHeaderAll(t *testing.T, h *ResponseHeader, key string, expectedValue [][]byte) {
	if len(h.PeekAll(key)) != len(expectedValue) {
		t.Fatalf("Unexpected size for key %q: %d. Expected %d", key, len(h.PeekAll(key)), len(expectedValue))
	}
	if !reflect.DeepEqual(h.PeekAll(key), expectedValue) {
		t.Fatalf("Unexpected value for key %q: %q. Expected %q", key, h.PeekAll(key), expectedValue)
	}
}

func TestRequestHeader_Keys(t *testing.T) {
	h := &RequestHeader{}
	h.Add(HeaderConnection, "keep-alive")
	h.Add("Content-Type", "aaa")
	err := h.SetTrailer("aaa,bbb,ccc")
	if err != nil {
		t.Fatal(err)
	}
	actualKeys := h.PeekKeys()
	expectedKeys := [][]byte{s2b("keep-alive"), s2b("aaa")}
	if reflect.DeepEqual(actualKeys, expectedKeys) {
		t.Fatalf("Unexpected value %q. Expected %q", actualKeys, expectedKeys)
	}
	actualTrailerKeys := h.PeekTrailerKeys()
	expectedTrailerKeys := [][]byte{s2b("aaa"), s2b("bbb"), s2b("ccc")}
	if reflect.DeepEqual(actualTrailerKeys, expectedTrailerKeys) {
		t.Fatalf("Unexpected value %q. Expected %q", actualTrailerKeys, expectedTrailerKeys)
	}
}

func TestResponseHeader_Keys(t *testing.T) {
	h := &ResponseHeader{}
	h.Add(HeaderConnection, "keep-alive")
	h.Add("Content-Type", "aaa")
	err := h.SetTrailer("aaa,bbb,ccc")
	if err != nil {
		t.Fatal(err)
	}
	actualKeys := h.PeekKeys()
	expectedKeys := [][]byte{s2b("keep-alive"), s2b("aaa")}
	if reflect.DeepEqual(actualKeys, expectedKeys) {
		t.Fatalf("Unexpected value %q. Expected %q", actualKeys, expectedKeys)
	}
	actualTrailerKeys := h.PeekTrailerKeys()
	expectedTrailerKeys := [][]byte{s2b("aaa"), s2b("bbb"), s2b("ccc")}
	if reflect.DeepEqual(actualTrailerKeys, expectedTrailerKeys) {
		t.Fatalf("Unexpected value %q. Expected %q", actualTrailerKeys, expectedTrailerKeys)
	}
}

func TestAddVaryHeader(t *testing.T) {
	t.Parallel()

	var h ResponseHeader

	h.addVaryBytes([]byte("Accept-Encoding"))
	got := string(h.Peek("Vary"))
	expected := "Accept-Encoding"
	if got != expected {
		t.Errorf("expected %q got %q", expected, got)
	}

	var buf bytes.Buffer
	if _, err := h.WriteTo(&buf); err != nil {
		t.Fatalf("unexpected error when writing header: %v", err)
	}

	if n := strings.Count(buf.String(), "Vary: "); n != 1 {
		t.Errorf("Vary occurred %d times", n)
	}
}

func TestAddVaryHeaderExisting(t *testing.T) {
	t.Parallel()

	var h ResponseHeader

	h.Set("Vary", "Accept")
	h.addVaryBytes([]byte("Accept-Encoding"))
	got := string(h.Peek("Vary"))
	expected := "Accept,Accept-Encoding"
	if got != expected {
		t.Errorf("expected %q got %q", expected, got)
	}

	var buf bytes.Buffer
	if _, err := h.WriteTo(&buf); err != nil {
		t.Fatalf("unexpected error when writing header: %v", err)
	}

	if n := strings.Count(buf.String(), "Vary: "); n != 1 {
		t.Errorf("Vary occurred %d times", n)
	}
}

func TestAddVaryHeaderExistingAcceptEncoding(t *testing.T) {
	t.Parallel()

	var h ResponseHeader

	h.Set("Vary", "Accept-Encoding")
	h.addVaryBytes([]byte("Accept-Encoding"))
	got := string(h.Peek("Vary"))
	expected := "Accept-Encoding"
	if got != expected {
		t.Errorf("expected %q got %q", expected, got)
	}

	var buf bytes.Buffer
	if _, err := h.WriteTo(&buf); err != nil {
		t.Fatalf("unexpected error when writing header: %v", err)
	}

	if n := strings.Count(buf.String(), "Vary: "); n != 1 {
		t.Errorf("Vary occurred %d times", n)
	}
}
