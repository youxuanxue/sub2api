package protocolrouter

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

type RequestDigest [sha256.Size]byte

type CanonicalRequestInput struct {
	InboundProtocol Protocol
	RequestedModel  string
	ResponsesPath   ResponsesPathKind
	Profile         RequestProfile
	Body            []byte
}

type CanonicalRequest struct {
	inboundProtocol Protocol
	requestedModel  string
	responsesPath   ResponsesPathKind
	profile         RequestProfile
	body            []byte
	digest          RequestDigest
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
		inboundProtocol: input.InboundProtocol,
		requestedModel:  model,
		responsesPath:   path,
		profile:         input.Profile,
		body:            body,
	}
	req.digest = digestRequest(req)
	return req, nil
}

func (r CanonicalRequest) InboundProtocol() Protocol        { return r.inboundProtocol }
func (r CanonicalRequest) RequestedModel() string           { return r.requestedModel }
func (r CanonicalRequest) ResponsesPath() ResponsesPathKind { return r.responsesPath }
func (r CanonicalRequest) Profile() RequestProfile          { return r.profile }
func (r CanonicalRequest) Digest() RequestDigest            { return r.digest }
func (r CanonicalRequest) Body() []byte                     { return append([]byte(nil), r.body...) }

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
	writeDigestBytes(&encoded, req.body)
	return sha256.Sum256(encoded.Bytes())
}

func writeDigestString(buf *bytes.Buffer, value string) {
	writeDigestBytes(buf, []byte(value))
}

func writeDigestBytes(buf *bytes.Buffer, value []byte) {
	_ = binary.Write(buf, binary.BigEndian, uint64(len(value)))
	_, _ = buf.Write(value)
}
