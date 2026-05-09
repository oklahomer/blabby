package connection

import "encoding/json"

// wsInboundEnvelope is the discriminator-only view of any inbound frame.
// Every frame the client sends MUST carry a "type" field; type-specific
// fields are decoded by the corresponding per-type struct in a second
// json.Unmarshal pass.
type wsInboundEnvelope struct {
	Type string `json:"type"`
}

// wsAuthFrame is the JSON shape of an "auth" frame.
type wsAuthFrame struct {
	Token string `json:"token"`
}

func decodeInboundFrame(data []byte) any {
	if len(data) == 0 {
		return &DecodeFailed{Reason: decodeFailureMalformedJSON}
	}
	var env wsInboundEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return &DecodeFailed{Reason: decodeFailureMalformedJSON}
	}

	switch env.Type {
	case "auth":
		var f wsAuthFrame
		if err := json.Unmarshal(data, &f); err != nil {
			return &DecodeFailed{Reason: decodeFailureMalformedJSON}
		}
		token, err := NewAuthToken(f.Token)
		if err != nil {
			return &ProtocolViolation{Reason: protocolViolationMissingToken}
		}
		return &InboundAuth{Token: token}
	case "pong":
		return &AppPongReceived{}
	default:
		return &DecodeFailed{Reason: decodeFailureUnknownType}
	}
}
