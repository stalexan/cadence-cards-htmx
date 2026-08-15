package yamlio

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// The topic format bundles a topic's Claude prompt configuration with,
// optionally, all of its decks and their cards. Unlike the card format it has
// no counterpart in the sibling Svelte app — it is Go-only, and nothing here
// may change how a bare card list is emitted or parsed. The per-deck Cards
// block is built by cardNodes and decoded by decodeCard, the same code the
// deck format uses, so the two stay byte-identical.
//
// A topic file is a mapping (Topic: / Decks:); a deck file is a sequence. That
// difference is what Detect keys on.

// TopicConfig is a topic's prompt configuration in YAML form. Field order is
// the emitted key order.
type TopicConfig struct {
	Name             string  `yaml:"Name"`
	TopicDescription *string `yaml:"TopicDescription"`
	Expertise        *string `yaml:"Expertise"`
	Focus            *string `yaml:"Focus"`
	ContextType      *string `yaml:"ContextType"`
	Example          *string `yaml:"Example"`
	Question         *string `yaml:"Question"`
}

// ExportDeck is one deck in a topic export: the deck's own settings plus its
// cards. Deck settings are carried explicitly (the card format infers them),
// so a bidirectional deck survives a round trip even without SM-2 params.
type ExportDeck struct {
	Name            string
	Field1Label     *string
	Field2Label     *string
	IsBidirectional bool
	Cards           []ExportCard
}

// TopicMetadata is the topic export's comment header.
type TopicMetadata struct {
	FormatVersion string
	TopicName     string
	CreatorName   *string
	ExportDate    string // YYYY-MM-DD
	DeckCount     int
	CardCount     int
}

// Ordered shapes for export. Decks is omitempty so a config-only export omits
// the key entirely rather than emitting an empty list.
type exportTopicFile struct {
	Topic TopicConfig  `yaml:"Topic"`
	Decks []exportDeck `yaml:"Decks,omitempty"`
}

type exportDeck struct {
	Name            string  `yaml:"Name"`
	Field1Label     *string `yaml:"Field1Label"`
	Field2Label     *string `yaml:"Field2Label"`
	IsBidirectional bool    `yaml:"IsBidirectional"`
	Cards           []any   `yaml:"Cards"`
}

// ExportTopic serializes a topic and, when includeDecks is set, its decks and
// cards. SM-2 parameters follow includeSM2 exactly as in Export; the comment
// header is emitted only when meta is non-nil.
func ExportTopic(cfg TopicConfig, decks []ExportDeck, meta *TopicMetadata, includeDecks, includeSM2 bool) (string, error) {
	file := exportTopicFile{Topic: cfg}
	if includeDecks {
		file.Decks = make([]exportDeck, len(decks))
		for i, d := range decks {
			file.Decks[i] = exportDeck{
				Name:            d.Name,
				Field1Label:     d.Field1Label,
				Field2Label:     d.Field2Label,
				IsBidirectional: d.IsBidirectional,
				Cards:           cardNodes(d.Cards, includeSM2),
			}
		}
	}

	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(file); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	content := sb.String()

	if meta != nil {
		creator := "Anonymous"
		if meta.CreatorName != nil && *meta.CreatorName != "" {
			creator = *meta.CreatorName
		}
		// headerValue for the same reason as the deck header: a newline in a
		// topic or creator name would otherwise escape the '# ' comment.
		header := fmt.Sprintf(`# ============================================
# Flashcard Topic Export
# ============================================
# Format Version: %s
# Topic: %s
# Creator: %s
# Exported: %s
# Decks: %d
# Cards: %d
# ============================================

`, meta.FormatVersion, headerValue(meta.TopicName), headerValue(creator), meta.ExportDate,
			meta.DeckCount, meta.CardCount)
		return header + content, nil
	}
	return content, nil
}

// ImportedDeck is one parsed deck. Invalid holds the cards that failed
// validation, so a bad card costs its card rather than the file.
type ImportedDeck struct {
	Name            string
	Field1Label     *string
	Field2Label     *string
	IsBidirectional bool
	Cards           []Card
	Invalid         []InvalidCard
}

// ImportedTopic is a parsed topic file. Errors holds deck-level problems
// (a deck with no name); per-card problems live on the deck.
type ImportedTopic struct {
	Config TopicConfig
	Decks  []ImportedDeck
	Errors []string
}

// Format identifies which of the two YAML shapes a document is.
type Format int

const (
	FormatUnknown Format = iota
	FormatCards          // a bare sequence of cards (the deck format)
	FormatTopic          // a mapping with a Topic key
)

// Detect reports which format src is, so callers can route it without parsing
// twice. A malformed document reports FormatUnknown; the importer the caller
// picks then produces the real parse error, keeping that message in one place.
func Detect(src string) Format {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		return FormatUnknown
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return FormatUnknown
	}
	switch node := root.Content[0]; node.Kind {
	case yaml.SequenceNode:
		return FormatCards
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == "Topic" {
				return FormatTopic
			}
		}
	}
	return FormatUnknown
}

// ImportTopic parses a topic export. The topic's name is required; everything
// else is optional. Bad cards and unnamed decks are reported rather than
// failing the file, mirroring Import's per-card leniency.
func ImportTopic(src string) (ImportedTopic, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		return ImportedTopic{}, fmt.Errorf("Error parsing YAML: %v", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return ImportedTopic{}, fmt.Errorf("Error parsing YAML: a topic export must be a mapping with a Topic key")
	}

	var topicNode, decksNode *yaml.Node
	doc := root.Content[0]
	for i := 0; i+1 < len(doc.Content); i += 2 {
		switch doc.Content[i].Value {
		case "Topic":
			topicNode = doc.Content[i+1]
		case "Decks":
			decksNode = doc.Content[i+1]
		}
	}
	if topicNode == nil {
		return ImportedTopic{}, fmt.Errorf("Error parsing YAML: a topic export must be a mapping with a Topic key")
	}

	var out ImportedTopic
	if err := topicNode.Decode(&out.Config); err != nil {
		return ImportedTopic{}, fmt.Errorf("Topic: %v", err)
	}
	out.Config.Name = strings.TrimSpace(out.Config.Name)
	if out.Config.Name == "" {
		return ImportedTopic{}, fmt.Errorf("Topic Name is required")
	}

	// Decks is optional: a config-only export omits it entirely.
	if decksNode == nil || decksNode.Kind == yaml.ScalarNode {
		return out, nil
	}
	if decksNode.Kind != yaml.SequenceNode {
		return ImportedTopic{}, fmt.Errorf("Error parsing YAML: Decks must be a list")
	}

	for i, node := range decksNode.Content {
		deck, err := decodeDeck(node, i)
		if err != nil {
			out.Errors = append(out.Errors, err.Error())
			continue
		}
		out.Decks = append(out.Decks, deck)
	}
	return out, nil
}

// decodeDeck decodes one deck node and its cards. Card errors are collected on
// the deck; only a missing deck name rejects the whole deck.
func decodeDeck(node *yaml.Node, index int) (ImportedDeck, error) {
	if node.Kind != yaml.MappingNode {
		return ImportedDeck{}, fmt.Errorf("Deck at index %d: must be a mapping", index)
	}

	var raw struct {
		Name            *string `yaml:"Name"`
		Field1Label     *string `yaml:"Field1Label"`
		Field2Label     *string `yaml:"Field2Label"`
		IsBidirectional *bool   `yaml:"IsBidirectional"`
	}
	if err := node.Decode(&raw); err != nil {
		return ImportedDeck{}, fmt.Errorf("Deck at index %d: %v", index, err)
	}
	if raw.Name == nil || strings.TrimSpace(*raw.Name) == "" {
		return ImportedDeck{}, fmt.Errorf("Deck at index %d: Name is required", index)
	}

	deck := ImportedDeck{
		Name:        strings.TrimSpace(*raw.Name),
		Field1Label: raw.Field1Label,
		Field2Label: raw.Field2Label,
	}
	if raw.IsBidirectional != nil {
		deck.IsBidirectional = *raw.IsBidirectional
	}

	// Cards is optional — an empty deck is legal.
	var cardsNode *yaml.Node
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == "Cards" {
			cardsNode = node.Content[i+1]
		}
	}
	if cardsNode == nil || cardsNode.Kind == yaml.ScalarNode {
		return deck, nil
	}
	if cardsNode.Kind != yaml.SequenceNode {
		return ImportedDeck{}, fmt.Errorf("Deck %q: Cards must be a list", deck.Name)
	}

	for i, cardNode := range cardsNode.Content {
		card, err := decodeCard(cardNode)
		if err != nil {
			var rawCard any
			_ = cardNode.Decode(&rawCard)
			deck.Invalid = append(deck.Invalid, InvalidCard{
				Card:  rawCard,
				Error: fmt.Sprintf("Deck %q, card at index %d: %v", deck.Name, i, err),
			})
			continue
		}
		deck.Cards = append(deck.Cards, card)
	}
	return deck, nil
}
