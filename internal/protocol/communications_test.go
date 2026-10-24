package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func commEnvelope() CommunicationsEnvelope {
	return CommunicationsEnvelope{Version: 1, Kind: "request", RequestID: "request-1", TargetID: "target-1", ProviderID: "provider-1", SessionID: "session-1", Generation: 1, Operation: "call", Arguments: json.RawMessage(`{"number":"example"}`)}
}
func commFrame(body string) []byte {
	b := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(b, uint32(len(body)))
	copy(b[4:], body)
	return b
}
func TestCommunicationsRoundTrip(t *testing.T) {
	for _, kind := range []string{"request", "reply"} {
		e := commEnvelope()
		e.Kind = kind
		if kind == "reply" {
			e.Arguments = nil
			e.Result = json.RawMessage(`null`)
		}
		var b bytes.Buffer
		if err := WriteCommunicationsEnvelope(&b, e, kind); err != nil {
			t.Fatal(err)
		}
		got, err := ReadCommunicationsEnvelope(&b, kind)
		if err != nil || got.RequestID != e.RequestID || got.Generation != 1 || got.Kind != kind {
			t.Fatalf("%+v %v", got, err)
		}
	}
}
func TestCommunicationsRejectEnvelope(t *testing.T) {
	base, _ := json.Marshal(commEnvelope())
	s := string(base)
	for name, body := range map[string]string{
		"duplicate": strings.Replace(s, `"version":1`, `"version":1,"version":1`, 1), "escaped duplicate": strings.Replace(s, `"version":1`, `"version":1,"vers\u0069on":1`, 1), "unknown": strings.Replace(s, `"version":1`, `"extra":null,"version":1`, 1), "forbidden null": strings.Replace(s, `"version":1`, `"result":null,"version":1`, 1), "missing": strings.Replace(s, `"version":1,`, "", 1), "nullversion": strings.Replace(s, `"version":1`, `"version":null`, 1), "wrongversion": strings.Replace(s, `"version":1`, `"version":2`, 1), "trailing": s + ` {}`, "array": "[]", "null": "null", "wrongkind": strings.Replace(s, `"request"`, `"reply"`, 1), "zerogeneration": strings.Replace(s, `"generation":1`, `"generation":0`, 1), "fraction": strings.Replace(s, `"generation":1`, `"generation":1.5`, 1), "operation": strings.Replace(s, `"call"`, `"unknown"`, 1), "utf8": string([]byte{'{', '"', 255, '"', ':', '1', '}'})} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadCommunicationsEnvelope(bytes.NewReader(commFrame(body)), "request"); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

type headerOnly struct {
	calls  int
	header []byte
}

func (h *headerOnly) Read(p []byte) (int, error) {
	h.calls++
	if h.calls > 1 {
		panic("read oversized body")
	}
	return copy(p, h.header), nil
}
func TestCommunicationsSizeBeforeBody(t *testing.T) {
	for _, n := range []uint32{0, communicationsArgumentsLimit + communicationsEnvelopeAllowance + 1, ^uint32(0)} {
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], n)
		r := &headerOnly{header: h[:]}
		if _, err := ReadCommunicationsEnvelope(r, "request"); err == nil || r.calls != 1 {
			t.Fatal(err, r.calls)
		}
	}
}
func TestCommunicationsTruncated(t *testing.T) {
	for _, b := range [][]byte{{0, 0}, append([]byte{0, 0, 0, 5}, '{', '}')} {
		if _, err := ReadCommunicationsEnvelope(bytes.NewReader(b), "request"); err == nil {
			t.Fatal("accepted")
		}
	}
}

type shortCommWriter struct {
	bytes.Buffer
	step int
	fail error
	zero bool
}

func (w *shortCommWriter) Write(p []byte) (int, error) {
	if w.zero {
		return 0, nil
	}
	if len(p) > w.step {
		p = p[:w.step]
	}
	n, _ := w.Buffer.Write(p)
	return n, w.fail
}
func TestCommunicationsShortWrites(t *testing.T) {
	w := &shortCommWriter{step: 3}
	if err := WriteCommunicationsEnvelope(w, commEnvelope(), "request"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCommunicationsEnvelope(&w.Buffer, "request"); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("partial")
	w = &shortCommWriter{step: 2, fail: sentinel}
	if err := WriteCommunicationsEnvelope(w, commEnvelope(), "request"); !errors.Is(err, sentinel) || w.Len() != 2 {
		t.Fatal(err, w.Len())
	}
	w = &shortCommWriter{zero: true}
	if err := WriteCommunicationsEnvelope(w, commEnvelope(), "request"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
}
func TestCommunicationsPayloadBounds(t *testing.T) {
	for _, kind := range []string{"request", "reply"} {
		limit := communicationsArgumentsLimit
		if kind == "reply" {
			limit = communicationsResultLimit
		}
		for _, extra := range []int{0, 1} {
			e := commEnvelope()
			e.Kind = kind
			payload := []byte(`{"x":"` + strings.Repeat("a", limit-8+extra) + `"}`)
			if len(payload) != limit+extra {
				t.Fatal(len(payload))
			}
			if kind == "reply" {
				e.Arguments = nil
				e.Result = payload
			} else {
				e.Arguments = payload
			}
			var b bytes.Buffer
			err := WriteCommunicationsEnvelope(&b, e, kind)
			if extra == 0 {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = ReadCommunicationsEnvelope(&b, kind); err != nil {
					t.Fatal(err)
				}
			} else if err == nil || b.Len() != 0 {
				t.Fatal("oversized sent")
			}
		}
	}
}
func TestCommunicationsInvalidWriteNoBytes(t *testing.T) {
	for _, mutate := range []func(*CommunicationsEnvelope){func(e *CommunicationsEnvelope) { e.TargetID = "" }, func(e *CommunicationsEnvelope) { e.Arguments = []byte(`[]`) }, func(e *CommunicationsEnvelope) { e.Result = []byte(`null`) }, func(e *CommunicationsEnvelope) { e.SessionID = " a" }, func(e *CommunicationsEnvelope) { e.RequestID = strings.Repeat("a", 257) }} {
		e := commEnvelope()
		mutate(&e)
		var b bytes.Buffer
		if WriteCommunicationsEnvelope(&b, e, "request") == nil || b.Len() != 0 {
			t.Fatal("invalid sent")
		}
	}
}

func TestCommunicationsUnescapedPayloadRoundTrip(t *testing.T) {
	for _, kind := range []string{"request", "reply"} {
		limit := communicationsArgumentsLimit
		if kind == "reply" {
			limit = communicationsResultLimit
		}
		for _, size := range []int{12008, limit} {
			for _, ch := range []string{"<", ">", "&"} {
				e := commEnvelope()
				e.Kind = kind
				payload := json.RawMessage(`{"x":"` + strings.Repeat(ch, size-8) + `"}`)
				if kind == "reply" {
					e.Arguments = nil
					e.Result = payload
				} else {
					e.Arguments = payload
				}
				var b bytes.Buffer
				if err := WriteCommunicationsEnvelope(&b, e, kind); err != nil {
					t.Fatal(kind, size, ch, err)
				}
				got, err := ReadCommunicationsEnvelope(&b, kind)
				if err != nil {
					t.Fatal(kind, size, ch, err)
				}
				actual := got.Arguments
				if kind == "reply" {
					actual = got.Result
				}
				if !bytes.Equal(actual, payload) {
					t.Fatal("payload changed")
				}
			}
		}
	}
}

func TestCommunicationsReaderPayloadAndDirections(t *testing.T) {
	for _, kind := range []string{"request", "reply"} {
		e := commEnvelope()
		e.Kind = kind
		limit := communicationsArgumentsLimit
		if kind == "reply" {
			limit = communicationsResultLimit
			e.Arguments = nil
		}
		payload := json.RawMessage(`{"x":"` + strings.Repeat("a", limit-7) + `"}`)
		if kind == "request" {
			e.Arguments = payload
		} else {
			e.Result = payload
		}
		body, _ := json.Marshal(e)
		if _, err := ReadCommunicationsEnvelope(bytes.NewReader(commFrame(string(body))), kind); err == nil {
			t.Fatal("oversize payload accepted")
		}
		e = commEnvelope()
		e.Kind = kind
		if kind == "reply" {
			e.Arguments = nil
			e.Result = []byte(`true`)
		}
		body, _ = json.Marshal(e)
		other := "reply"
		if kind == "reply" {
			other = "request"
		}
		if _, err := ReadCommunicationsEnvelope(bytes.NewReader(commFrame(string(body))), other); err == nil {
			t.Fatal("wrong direction")
		}
	}
	e := commEnvelope()
	e.Kind = "reply"
	e.Arguments = nil
	e.Result = []byte(`[]`)
	b, _ := json.Marshal(e)
	s := strings.Replace(string(b), `"version":1`, `"version":1,"arguments":null`, 1)
	if _, err := ReadCommunicationsEnvelope(bytes.NewReader(commFrame(s)), "reply"); err == nil {
		t.Fatal("forbidden reply field")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], communicationsResultLimit+communicationsEnvelopeAllowance+1)
	if _, err := ReadCommunicationsEnvelope(&headerOnly{header: h[:]}, "reply"); err == nil {
		t.Fatal("reply oversized")
	}
}

type invalidCommWriter int

func (w invalidCommWriter) Write(p []byte) (int, error) {
	if w < 0 {
		return -1, nil
	}
	return len(p) + 1, nil
}
func TestCommunicationsInvalidWriteCounts(t *testing.T) {
	for _, w := range []invalidCommWriter{-1, 1} {
		if err := WriteCommunicationsEnvelope(w, commEnvelope(), "request"); !errors.Is(err, io.ErrShortWrite) {
			t.Fatal(err)
		}
	}
}
func TestCommunicationsUnicodeAndScalars(t *testing.T) {
	for _, raw := range []string{`null`, `true`, `42`, `"text"`, `[]`, `{}`} {
		e := commEnvelope()
		e.Kind = "reply"
		e.Arguments = nil
		e.Result = []byte(raw)
		var b bytes.Buffer
		if err := WriteCommunicationsEnvelope(&b, e, "reply"); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCommunicationsEnvelope(&b, "reply"); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []rune{'\u2028', '\u2029'} {
		e := commEnvelope()
		e.Arguments = []byte(`{"x":"` + strings.Repeat(string(r), (communicationsArgumentsLimit-8)/3) + `"}`)
		e.RequestID = "请求😀"
		var b bytes.Buffer
		if err := WriteCommunicationsEnvelope(&b, e, "request"); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCommunicationsEnvelope(&b, "request"); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(commEnvelope())
	for _, id := range []string{`\ud800`, `\udc00`, `\ud800x`, `\ud800\u0041`} {
		s := strings.Replace(string(b), "request-1", id, 1)
		if _, err := ReadCommunicationsEnvelope(bytes.NewReader(commFrame(s)), "request"); err == nil {
			t.Fatal("invalid surrogate", id)
		}
	}
	for _, id := range []string{`\ud83d\ude00`, `\\ud800`, strings.Repeat("a", 256)} {
		s := strings.Replace(string(b), "request-1", id, 1)
		if _, err := ReadCommunicationsEnvelope(bytes.NewReader(commFrame(s)), "request"); err != nil {
			t.Fatal(id, err)
		}
	}
}
