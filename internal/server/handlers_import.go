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
	deckID := int64(formInt(r, "deckId", 0))
	if yamlContent == "" || deckID == 0 {
		s.fragment(w, http.StatusUnprocessableEntity, "import_result",
			importResultData{Message: "YAML content and a target deck are required."})
		return
	}
	if len(yamlContent) > maxImportSize {
		s.fragment(w, http.StatusUnprocessableEntity, "import_result",
			importResultData{Message: "YAML content exceeds the 1 MB limit."})
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
