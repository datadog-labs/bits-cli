package spec

// Handoff is the client tool Bits web uses to hand a conversation to Bits CLI.
const Handoff = "bits_cli_handoff"

var ClientHandoff = Identity{ClientSide: true, Name: Handoff}

type HandoffInput struct {
	Reason string `json:"reason"`
}
