package server

import (
	"errors"
	"net/http"

	"cadence-cards/internal/sm2"
	"cadence-cards/internal/store"
	"cadence-cards/internal/yamlio"
)

// maxImportSize matches the source's 1 MB YAML limit.
const maxImportSize = 1 << 20

// importPageData feeds import.html.
type importPageData struct {
	Decks []store.Deck
}

// importResultData feeds the import_result.html fragment.
type importResultData struct {
	Success       bool
	Message       string
	ImportedCount int
	FailedCount   int
	Errors        []string
	DeckName      string
	// MadeBidirectional is set when the YAML's reverse SM-2 params switched
	// a unidirectional deck over, so the user knows the deck changed.
	MadeBidirectional bool

	// Topic-import fields, set only when the file was a topic export.
	IsTopic      bool
	TopicID      int64
	TopicName    string
	TopicRenamed bool
	DeckCount    int
}

func (s *Server) handleImportPage(w http.ResponseWriter, r *http.Request) {
	decks, err := s.store.ListDecks(r.Context(), userFrom(r).ID, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "import", importPageData{Decks: decks})
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	userID := userFrom(r).ID
	// Import failures render as 422 fragments: the htmx-config in base.html
	// swaps 422 (and 409) but discards other 4xx/5xx bodies, so any other
	// status would leave the user with no feedback at all. The wire cap is 3×
	// the content limit because the textarea arrives urlencoded (up to 3 bytes
	// per byte); the decoded length check below is the real 1 MB limit.
	r.Body = http.MaxBytesReader(w, r.Body, 3*maxImportSize+4096)
	if err := r.ParseForm(); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.fragment(w, http.StatusUnprocessableEntity, "import_result",
				importResultData{Message: "YAML content exceeds the 1 MB limit."})
			return
		}
		s.fragment(w, http.StatusUnprocessableEntity, "import_result",
			importResultData{Message: "Invalid form submission."})
		return
	}

	yamlContent := r.FormValue("yamlContent")
	if yamlContent == "" {
		s.fragment(w, http.StatusUnprocessableEntity, "import_result",
			importResultData{Message: "YAML content is required."})
		return
	}
	if len(yamlContent) > maxImportSize {
		s.fragment(w, http.StatusUnprocessableEntity, "import_result",
			importResultData{Message: "YAML content exceeds the 1 MB limit."})
		return
	}

	// A topic export is a mapping, a card list is a sequence — so the file
	// says which kind of import this is and the target deck is only consulted
	// for the latter.
	switch yamlio.Detect(yamlContent) {
	case yamlio.FormatTopic:
		s.importTopic(w, r, userID, yamlContent)
		return
	case yamlio.FormatCards:
		s.importCards(w, r, userID, yamlContent)
		return
	default:
		s.fragment(w, http.StatusUnprocessableEntity, "import_result",
			importResultData{Message: "Unrecognized YAML: expected a list of cards or a topic export."})
		return
	}
}

// importCards handles the card-list format: cards go into an existing deck.
func (s *Server) importCards(w http.ResponseWriter, r *http.Request, userID int64, yamlContent string) {
	deckID := int64(formInt(r, "deckId", 0))
	if deckID == 0 {
		s.fragment(w, http.StatusUnprocessableEntity, "import_result",
			importResultData{Message: "A target deck is required for a card import."})
		return
	}

	deck, err := s.store.GetDeck(r.Context(), userID, deckID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fragment(w, http.StatusUnprocessableEntity, "import_result",
				importResultData{Message: "The selected deck no longer exists."})
			return
		}
		s.serverError(w, r, err)
		return
	}

	valid, invalid, err := yamlio.Import(yamlContent)
	if err != nil {
		s.fragment(w, http.StatusUnprocessableEntity, "import_result",
			importResultData{Message: err.Error()})
		return
	}

	result := importResultData{DeckName: deck.Name, FailedCount: len(invalid)}
	for _, inv := range invalid {
		result.Errors = append(result.Errors, inv.Error)
	}

	params := make([]store.ImportCardParams, 0, len(valid))
	for _, c := range valid {
		fwd, err := c.ForwardState()
		if err != nil {
			result.FailedCount++
			result.Errors = append(result.Errors, "Card "+c.Front+": "+err.Error())
			continue
		}
		rev, err := c.ReverseState()
		if err != nil {
			result.FailedCount++
			result.Errors = append(result.Errors, "Card "+c.Front+": "+err.Error())
			continue
		}
		params = append(params, store.ImportCardParams{
			Front:    c.Front,
			Back:     c.Back,
			Note:     c.Note,
			Priority: sm2.Priority(c.Priority),
			Tags:     c.Tags,
			Forward:  fwd,
			Reverse:  rev,
		})
	}

	if len(params) > 0 {
		madeBidirectional, err := s.store.ImportCards(r.Context(), userID, deckID, params)
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		result.MadeBidirectional = madeBidirectional
	}

	result.Success = len(params) > 0
	result.ImportedCount = len(params)
	if result.Success {
		result.Message = "Import complete."
	} else {
		result.Message = "No valid cards to import."
	}
	s.fragment(w, http.StatusOK, "import_result", result)
}

// importTopic handles the topic format: a new topic is created, along with any
// decks and cards the file carries. The target-deck select is ignored.
func (s *Server) importTopic(w http.ResponseWriter, r *http.Request, userID int64, yamlContent string) {
	parsed, err := yamlio.ImportTopic(yamlContent)
	if err != nil {
		s.fragment(w, http.StatusUnprocessableEntity, "import_result",
			importResultData{Message: err.Error()})
		return
	}

	result := importResultData{IsTopic: true, Errors: parsed.Errors, FailedCount: len(parsed.Errors)}

	p := store.TopicImportParams{
		Topic: store.TopicParams{
			Name:             parsed.Config.Name,
			TopicDescription: parsed.Config.TopicDescription,
			Expertise:        parsed.Config.Expertise,
			Focus:            parsed.Config.Focus,
			ContextType:      parsed.Config.ContextType,
			Example:          parsed.Config.Example,
			Question:         parsed.Config.Question,
		},
	}

	for _, d := range parsed.Decks {
		for _, inv := range d.Invalid {
			result.FailedCount++
			result.Errors = append(result.Errors, inv.Error)
		}
		deck := store.DeckImportParams{
			Name:            d.Name,
			Field1Label:     d.Field1Label,
			Field2Label:     d.Field2Label,
			IsBidirectional: d.IsBidirectional,
		}
		for _, c := range d.Cards {
			fwd, err := c.ForwardState()
			if err != nil {
				result.FailedCount++
				result.Errors = append(result.Errors, "Card "+c.Front+": "+err.Error())
				continue
			}
			rev, err := c.ReverseState()
			if err != nil {
				result.FailedCount++
				result.Errors = append(result.Errors, "Card "+c.Front+": "+err.Error())
				continue
			}
			deck.Cards = append(deck.Cards, store.ImportCardParams{
				Front:    c.Front,
				Back:     c.Back,
				Note:     c.Note,
				Priority: sm2.Priority(c.Priority),
				Tags:     c.Tags,
				Forward:  fwd,
				Reverse:  rev,
			})
		}
		p.Decks = append(p.Decks, deck)
	}

	res, err := s.store.ImportTopic(r.Context(), userID, p)
	if err != nil {
		// ErrDuplicate would otherwise reach storeError, which maps it to a
		// bare 500 that htmx discards — leaving the user with no feedback.
		if errors.Is(err, store.ErrDuplicate) {
			s.fragment(w, http.StatusUnprocessableEntity, "import_result",
				importResultData{Message: "Could not find an available name for this topic."})
			return
		}
		s.storeError(w, r, err)
		return
	}

	result.Success = true
	result.Message = "Import complete."
	result.TopicID = res.TopicID
	result.TopicName = res.TopicName
	result.TopicRenamed = res.Renamed
	result.DeckCount = res.DeckCount
	result.ImportedCount = res.CardCount
	result.Errors = append(result.Errors, res.DeckRenames...)
	s.fragment(w, http.StatusOK, "import_result", result)
}
