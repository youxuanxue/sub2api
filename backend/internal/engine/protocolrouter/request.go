package protocolrouter

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicpolicy"
)

type RequestDigest [sha256.Size]byte

type CanonicalRequestInput struct {
	InboundProtocol Protocol
	RequestedModel  string
	ResponsesPath   ResponsesPathKind
	Profile         RequestProfile
	Body            []byte
	// BodyJSONValidated records that the caller already decoded Body as JSON, so
	// per-route compatibility work may skip re-validating it. Leave it false
	// unless the body came from a successful decode of these exact bytes; a
	// wrong true would let an invalid body reach the rewrite helpers.
	BodyJSONValidated bool
}

type CanonicalRequest struct {
	inboundProtocol   Protocol
	requestedModel    string
	responsesPath     ResponsesPathKind
	profile           RequestProfile
	body              []byte
	digest            RequestDigest
	bodyJSONValidated bool
	// policyFacts holds the body-derived half of the per-route compatibility
	// decision. It is derived here, once, and only when bodyJSONValidated proves
	// the bytes decode, so an unproven body still reaches the validating path.
	policyFacts anthropicpolicy.Facts
	// memo caches derivations that depend only on this immutable request. Every
	// copy of one constructed request shares it, so candidate selection derives
	// them once per request instead of once per account.
	memo *requestMemo
}

type requestMemo struct {
	downgradeOnce sync.Once
	downgrade     downgradeOutcome
}

type downgradeOutcome struct {
	request    CanonicalRequest
	adjustment string
	ok         bool
}

func NewCanonicalRequest(input CanonicalRequestInput) (CanonicalRequest, error) {
	if !input.InboundProtocol.Valid() {
		return CanonicalRequest{}, fmt.Errorf("invalid inbound protocol %q", input.InboundProtocol)
	}
	model := strings.TrimSpace(input.RequestedModel)
	if model == "" {
		return CanonicalRequest{}, errors.New("requested model is required")
	}
	path := input.ResponsesPath
	if input.InboundProtocol == ProtocolResponses {
		if path == ResponsesPathNone {
			path = ResponsesPathRoot
		}
		if !path.Valid() {
			return CanonicalRequest{}, fmt.Errorf("invalid responses path %q", path)
		}
	} else {
		path = ResponsesPathNone
	}
	body := append([]byte(nil), input.Body...)
	if len(body) == 0 {
		return CanonicalRequest{}, errors.New("request body is required")
	}
	req := CanonicalRequest{
		inboundProtocol:   input.InboundProtocol,
		requestedModel:    model,
		responsesPath:     path,
		profile:           input.Profile,
		body:              body,
		bodyJSONValidated: input.BodyJSONValidated,
		memo:              &requestMemo{},
	}
	req.digest = digestRequest(req)
	if req.bodyJSONValidated {
		req.policyFacts = anthropicpolicy.InspectValidated(req.body, req.inboundProtocol == ProtocolMessages)
	}
	return req, nil
}

func (r CanonicalRequest) InboundProtocol() Protocol        { return r.inboundProtocol }
func (r CanonicalRequest) RequestedModel() string           { return r.requestedModel }
func (r CanonicalRequest) ResponsesPath() ResponsesPathKind { return r.responsesPath }
func (r CanonicalRequest) Profile() RequestProfile          { return r.profile }
func (r CanonicalRequest) Digest() RequestDigest            { return r.digest }
func (r CanonicalRequest) Body() []byte                     { return append([]byte(nil), r.body...) }

// BodyEquals reports whether body is the exact document already stored on the
// request. Callers use it to skip a second full unmarshal of a body that
// admission already decoded.
func (r CanonicalRequest) BodyEquals(body []byte) bool {
	return bytes.Equal(r.body, body)
}

// Matches reports whether NewCanonicalRequest(input) would build exactly r.
// Every field NewCanonicalRequest stores is compared after the same
// normalization, and the digest and policy facts are pure functions of those
// fields, so a caller holding r can keep it (and its memo) instead of copying
// and hashing the same body again.
func (r CanonicalRequest) Matches(input CanonicalRequestInput) bool {
	if r.memo == nil || input.InboundProtocol != r.inboundProtocol ||
		strings.TrimSpace(input.RequestedModel) != r.requestedModel ||
		input.Profile != r.profile || input.BodyJSONValidated != r.bodyJSONValidated {
		return false
	}
	path := input.ResponsesPath
	if r.inboundProtocol == ProtocolResponses {
		if path == ResponsesPathNone {
			path = ResponsesPathRoot
		}
	} else {
		path = ResponsesPathNone
	}
	return path == r.responsesPath && bytes.Equal(r.body, input.Body)
}

func digestRequest(req CanonicalRequest) RequestDigest {
	var encoded bytes.Buffer
	writeDigestString(&encoded, string(req.inboundProtocol))
	writeDigestString(&encoded, req.requestedModel)
	writeDigestString(&encoded, string(req.responsesPath))
	if req.profile.Stream {
		_ = encoded.WriteByte(1)
	} else {
		_ = encoded.WriteByte(0)
	}
	if req.profile.Tools {
		_ = encoded.WriteByte(1)
	} else {
		_ = encoded.WriteByte(0)
	}
	writeDigestString(&encoded, string(req.profile.ToolChoice))
	writeDigestString(&encoded, string(req.profile.Continuation))
	writeDigestString(&encoded, string(req.profile.Reasoning))
	writeDigestString(&encoded, string(req.profile.PromptCache))
	_ = binary.Write(&encoded, binary.BigEndian, uint32(req.profile.ContentKinds))
	_ = binary.Write(&encoded, binary.BigEndian, uint64(len(req.body)))
	// Preserve the length-prefixed wire digest without copying the entire body
	// into a second buffer. The canonical request already owns immutable bytes.
	hash := sha256.New()
	_, _ = hash.Write(encoded.Bytes())
	_, _ = hash.Write(req.body)
	var digest RequestDigest
	hash.Sum(digest[:0])
	return digest
}

func writeDigestString(buf *bytes.Buffer, value string) {
	writeDigestBytes(buf, []byte(value))
}

func writeDigestBytes(buf *bytes.Buffer, value []byte) {
	_ = binary.Write(buf, binary.BigEndian, uint64(len(value)))
	_, _ = buf.Write(value)
}
