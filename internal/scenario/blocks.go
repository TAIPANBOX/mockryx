package scenario

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// This file holds the two wire forms of a Message's content. A plain string
// is the form every scenario written before 2026-10-12 uses, and it must
// marshal exactly as it always did.

// MarshalJSON writes the message in the Anthropic wire shape: content is the
// plain string when the message has no blocks, and the block list when it does.
func (m Message) MarshalJSON() ([]byte, error) {
	if len(m.Blocks) > 0 {
		return json.Marshal(struct {
			Role    string  `json:"role"`
			Content []Block `json:"content"`
		}{m.Role, m.Blocks})
	}
	return json.Marshal(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{m.Role, m.Content})
}

// UnmarshalJSON accepts content as a string or as a list of blocks.
func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = Message{Role: raw.Role}
	if len(raw.Content) == 0 || string(raw.Content) == "null" {
		return nil
	}
	if raw.Content[0] == '[' {
		return json.Unmarshal(raw.Content, &m.Blocks)
	}
	return json.Unmarshal(raw.Content, &m.Content)
}

// UnmarshalYAML accepts content as a string or as a list of blocks.
func (m *Message) UnmarshalYAML(n *yaml.Node) error {
	var raw struct {
		Role    string    `yaml:"role"`
		Content yaml.Node `yaml:"content"`
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	*m = Message{Role: raw.Role}
	switch raw.Content.Kind {
	case 0: // content omitted
		return nil
	case yaml.ScalarNode:
		return raw.Content.Decode(&m.Content)
	case yaml.SequenceNode:
		return raw.Content.Decode(&m.Blocks)
	default:
		return fmt.Errorf("line %d: message content must be a string or a list of blocks", raw.Content.Line)
	}
}

// MarshalJSON writes the block in the Anthropic wire shape for its type. A
// tool_use always carries input, even when it is empty, because the wire
// requires the key.
func (b Block) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case "tool_use":
		in := b.Input
		if in == nil {
			in = map[string]any{}
		}
		return json.Marshal(struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}{b.Type, b.ID, b.Name, in})
	case "tool_result":
		return json.Marshal(struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			Content   string `json:"content"`
			IsError   bool   `json:"is_error,omitempty"`
		}{b.Type, b.ToolUseID, b.Content, b.IsError})
	default:
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{b.Type, b.Text})
	}
}
