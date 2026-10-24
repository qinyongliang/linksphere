package protocol

// This bounded codec is not registered with AgentHello, StreamRequest, or any
// endpoint. Correlation fields are continuity data, never proof of authorization.
import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const CommunicationsVersion = 1
const communicationsArgumentsLimit = 64 * 1024
const communicationsResultLimit = 1024 * 1024
const communicationsEnvelopeAllowance = 8192

var ErrCommunicationsEnvelope = errors.New("invalid communications envelope")

// One exchange owns one stream. A caller must compare every correlation field
// against its pinned request and reauthorize; this codec does neither for it.
type CommunicationsEnvelope struct {
	Version    uint32          `json:"version"`
	Kind       string          `json:"kind"`
	RequestID  string          `json:"requestId"`
	TargetID   string          `json:"targetId"`
	ProviderID string          `json:"providerId"`
	SessionID  string          `json:"sessionId"`
	Generation uint64          `json:"generation"`
	Operation  string          `json:"operation"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
}

func communicationsID(s string) bool {
	return s != "" && len(s) <= 256 && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func communicationsLimit(kind string) int {
	switch kind {
	case "request":
		return communicationsArgumentsLimit + communicationsEnvelopeAllowance
	case "reply":
		return communicationsResultLimit + communicationsEnvelopeAllowance
	}
	return 0
}
func (e CommunicationsEnvelope) valid(kind string) bool {
	if communicationsLimit(kind) == 0 || e.Kind != kind || e.Version != CommunicationsVersion || e.Generation == 0 ||
		!communicationsID(e.RequestID) || !communicationsID(e.TargetID) || !communicationsID(e.ProviderID) || !communicationsID(e.SessionID) {
		return false
	}
	if e.Operation != "call" && e.Operation != "audio" && e.Operation != "sms" {
		return false
	}
	if kind == "request" {
		a := bytes.TrimSpace(e.Arguments)
		return len(e.Result) == 0 && len(e.Arguments) <= communicationsArgumentsLimit && len(a) > 0 && a[0] == '{' && utf8.Valid(e.Arguments) && json.Valid(e.Arguments)
	}
	return len(e.Arguments) == 0 && len(e.Result) > 0 && len(e.Result) <= communicationsResultLimit && utf8.Valid(e.Result) && json.Valid(e.Result)
}

// ReadCommunicationsEnvelope rejects the declared size before allocating. The
// caller supplies the expected direction. Deadlines and stream ownership belong
// to the transport adapter; a short read is an error, never a retry request.
func ReadCommunicationsEnvelope(r io.Reader, kind string) (CommunicationsEnvelope, error) {
	var zero CommunicationsEnvelope
	limit := communicationsLimit(kind)
	if r == nil || limit == 0 {
		return zero, ErrCommunicationsEnvelope
	}
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return zero, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || uint64(n) > uint64(limit) {
		return zero, ErrCommunicationsEnvelope
	}
	body := make([]byte, int(n))
	if _, err := io.ReadFull(r, body); err != nil {
		return zero, err
	}
	if !utf8.Valid(body) {
		return zero, ErrCommunicationsEnvelope
	}
	// Parse raw members separately so encoding/json's last-duplicate-wins behavior
	// cannot silently replace a correlation field. Unknown top-level fields fail.
	d := json.NewDecoder(bytes.NewReader(body))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return zero, ErrCommunicationsEnvelope
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return zero, ErrCommunicationsEnvelope
		}
		name, ok := key.(string)
		if !ok {
			return zero, ErrCommunicationsEnvelope
		}
		switch name {
		case "version", "kind", "requestId", "targetId", "providerId", "sessionId", "generation", "operation", "arguments", "result":
		default:
			return zero, ErrCommunicationsEnvelope
		}
		if _, exists := fields[name]; exists {
			return zero, ErrCommunicationsEnvelope
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return zero, ErrCommunicationsEnvelope
		}
		fields[name] = value
	}
	if t, err = d.Token(); err != nil || t != json.Delim('}') {
		return zero, ErrCommunicationsEnvelope
	}
	if _, err = d.Token(); err != io.EOF {
		return zero, ErrCommunicationsEnvelope
	}
	// Reject forbidden fields even when explicitly null, and require all fields.
	required := []string{"version", "kind", "requestId", "targetId", "providerId", "sessionId", "generation", "operation"}
	forbidden := "result"
	if kind == "request" {
		required = append(required, "arguments")
	} else {
		required = append(required, "result")
		forbidden = "arguments"
	}
	if _, exists := fields[forbidden]; exists {
		return zero, ErrCommunicationsEnvelope
	}
	for _, key := range required {
		if _, exists := fields[key]; !exists {
			return zero, ErrCommunicationsEnvelope
		}
	}
	for _, key := range []string{"requestId", "targetId", "providerId", "sessionId"} {
		if !communicationsScalarString(fields[key]) {
			return zero, ErrCommunicationsEnvelope
		}
	}
	if err = json.Unmarshal(body, &zero); err != nil || !zero.valid(kind) {
		return CommunicationsEnvelope{}, ErrCommunicationsEnvelope
	}
	return zero, nil
}

// WriteCommunicationsEnvelope validates and serializes before its first write.
// A returned error can follow partial transmission: callers MUST NOT auto-retry.
// Short successful writes are completed; zero-progress or invalid counts fail.
func WriteCommunicationsEnvelope(w io.Writer, e CommunicationsEnvelope, kind string) error {
	if w == nil || !e.valid(kind) {
		return ErrCommunicationsEnvelope
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(e)
	body := bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))
	if err != nil || len(body) > communicationsLimit(kind) {
		return ErrCommunicationsEnvelope
	}
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if n < 0 || n > len(frame) {
			return io.ErrShortWrite
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

// encoding/json otherwise replaces unpaired surrogate escapes with U+FFFD,
// collapsing distinct wire identifiers before correlation. Reject that spelling.
func communicationsScalarString(raw []byte) bool {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return false
	}
	hex4 := func(p []byte) int {
		if len(p) < 4 {
			return -1
		}
		n := 0
		for _, c := range p[:4] {
			n *= 16
			switch {
			case c >= '0' && c <= '9':
				n += int(c - '0')
			case c >= 'a' && c <= 'f':
				n += int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				n += int(c-'A') + 10
			default:
				return -1
			}
		}
		return n
	}
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw)-1 {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n := hex4(raw[i+1 : i+5])
		if n < 0 {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low := hex4(raw[i+3 : i+7])
			if low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
