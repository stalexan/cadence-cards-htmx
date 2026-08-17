package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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

// importDetectData feeds the import_detect.html fragment: a live read of what
// the user has pasted, so the page can say which kind of import this will be
// before they submit.
type importDetectData struct {
	// Format is "topic", "cards", "unknown", or "" for an empty box. app.js
	// keys the deck field's visibility off it, so it is the fragment's
	// contract with the client as well as its rendering switch.
	Format string
	// Summary is the human sentence; Detail adds counts when we have them.
	Summary string
	Detail  string
	// Problem renders the hint as a warning instead of a confirmation.
	Problem bool
}

// handleImportDetect previews the format of the pasted YAML. It runs the same
// yamlio.Detect and parsers the POST handler will run, rather than sniffing the
// text in JavaScript, so the hint cannot disagree with the actual import.
func (s *Server) handleImportDetect(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 3*maxImportSize+4096)
	if err := r.ParseForm(); err != nil {
		s.fragment(w, http.StatusOK, "import_detect", importDetectData{
			Format: "unknown", Problem: true,
			Summary: "That is more than the 1 MB import limit.",
		})
		return
	}

	content := r.FormValue("yamlContent")
	if strings.TrimSpace(content) == "" {
		// Empty box: render nothing and let the deck field come back.
		s.fragment(w, http.StatusOK, "import_detect", importDetectData{})
		return
	}

	switch yamlio.Detect(content) {
	case yamlio.FormatTopic:
		parsed, err := yamlio.ImportTopic(content)
		if err != nil {
			s.fragment(w, http.StatusOK, "import_detect", importDetectData{
				Format: "topic", Problem: true,
				Summary: "This looks like a topic export, but it could not be read.",
				Detail:  err.Error(),
			})
			return
		}
		cards := 0
		for _, d := range parsed.Decks {
			cards += len(d.Cards)
		}
		s.fragment(w, http.StatusOK, "import_detect", importDetectData{
			Format:  "topic",
			Summary: "Topic export detected: " + parsed.Config.Name,
			Detail:  topicDetail(len(parsed.Decks), cards) + provenanceDetail(parsed.Provenance),
		})
	case yamlio.FormatCards:
		valid, invalid, err := yamlio.Import(content)
		if err != nil {
			s.fragment(w, http.StatusOK, "import_detect", importDetectData{
				Format: "cards", Problem: true,
				Summary: "This looks like a card list, but it could not be read.",
				Detail:  err.Error(),
			})
			return
		}
		detail := plural(len(valid), "card", "cards") + " ready to import."
		if len(invalid) > 0 {
			detail += " " + plural(len(invalid), "card", "cards") + " will be skipped."
		}
		s.fragment(w, http.StatusOK, "import_detect", importDetectData{
			Format:  "cards",
			Summary: "Card list detected.",
			Detail:  detail,
		})
	default:
		s.fragment(w, http.StatusOK, "import_detect", importDetectData{
			Format: "unknown", Problem: true,
			Summary: "This does not look like a card list or a topic export.",
			// Not "starts with Topic:" — a topic file may lead with its
			// Provenance block, and Detect looks for the key rather than the
			// first line.
			Detail: "A card list starts with “- Front:”; a topic export has a “Topic:” block.",
		})
	}
}

// topicDetail describes a topic file's contents, including the config-only case
// (a topic exported with its decks left out).
func topicDetail(decks, cards int) string {
	if decks == 0 {
		return "Settings only — no decks or cards in this file."
	}
	return plural(decks, "deck", "decks") + ", " + plural(cards, "card", "cards") + "."
}

// provenanceDetail appends a file's attribution to the import hint. Terms of
// reuse are worth seeing *before* the import rather than on the topic page
// afterwards, which is the whole reason the hint is rendered server-side from
// the same parse the import will run. Source is left out — a URL is too long
// for one line of hint, and it is visible in the YAML box above it.
func provenanceDetail(p yamlio.Provenance) string {
	var parts []string
	if p.Author != nil {
		parts = append(parts, "by "+*p.Author)
	}
	if p.License != nil {
		parts = append(parts, "licensed "+*p.License)
	}
	if len(parts) == 0 {
		return ""
	}
	return " Shared " + strings.Join(parts, ", ") + "."
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
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

// topicImportParams maps a parsed topic file onto store params, recording any
// per-card failures on result as it goes.
//
// Shared by the pasted-file route and the /samples route so a bundled sample
// travels exactly the path a pasted file does — there is no second, quieter
// importer that could accept something the real one rejects.
func topicImportParams(parsed yamlio.ImportedTopic, result *importResultData) store.TopicImportParams {
	p := store.TopicImportParams{
		Topic: store.TopicParams{
			Name:             parsed.Config.Name,
			TopicDescription: parsed.Config.TopicDescription,
			Expertise:        parsed.Config.Expertise,
			Focus:            parsed.Config.Focus,
			ContextType:      parsed.Config.ContextType,
			Example:          parsed.Config.Example,
			Question:         parsed.Config.Question,
			// Carried through verbatim: dropping attribution here would make a
			// shared deck lose its author and licence the moment it entered the
			// app, and re-exporting it would silently launder the file.
			Author:  parsed.Provenance.Author,
			License: parsed.Provenance.License,
			Source:  parsed.Provenance.Source,
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
	return p
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
	p := topicImportParams(parsed, &result)

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
