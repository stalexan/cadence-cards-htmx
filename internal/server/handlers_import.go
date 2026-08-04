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
	r.Body = http.MaxBytesReader(w, r.Body, maxImportSize+4096)
	if err := r.ParseForm(); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.fragment(w, http.StatusRequestEntityTooLarge, "import_result",
				importResultData{Message: "YAML content exceeds the 1 MB limit."})
			return
		}
		s.fragment(w, http.StatusBadRequest, "import_result",
			importResultData{Message: "Invalid form submission."})
		return
	}

	yamlContent := r.FormValue("yamlContent")
	deckID := int64(formInt(r, "deckId", 0))
	if yamlContent == "" || deckID == 0 {
		s.fragment(w, http.StatusBadRequest, "import_result",
			importResultData{Message: "YAML content and a target deck are required."})
		return
	}
	if len(yamlContent) > maxImportSize {
		s.fragment(w, http.StatusRequestEntityTooLarge, "import_result",
			importResultData{Message: "YAML content exceeds the 1 MB limit."})
		return
	}

	deck, err := s.store.GetDeck(r.Context(), userID, deckID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	valid, invalid, err := yamlio.Import(yamlContent)
	if err != nil {
		s.fragment(w, http.StatusBadRequest, "import_result",
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
		if err := s.store.ImportCards(r.Context(), userID, deckID, params); err != nil {
			s.storeError(w, r, err)
			return
		}
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
