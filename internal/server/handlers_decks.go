package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cadence-cards/internal/store"
	"cadence-cards/internal/yamlio"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func deckParamsFromForm(r *http.Request) store.DeckParams {
	topicID, _ := strconv.ParseInt(formStr(r, "topicId"), 10, 64)
	return store.DeckParams{
		Name:            formStr(r, "name"),
		TopicID:         topicID,
		Field1Label:     formStrPtr(r, "field1Label"),
		Field2Label:     formStrPtr(r, "field2Label"),
		IsBidirectional: formStr(r, "isBidirectional") == "on",
	}
}

// deckFormData feeds decks_form.html.
type deckFormData struct {
	Deck   *store.Deck
	Topics []store.Topic
	Params store.DeckParams
	Error  string
}

// deckGridData feeds the deck_grid partial on /decks.
type deckGridData struct {
	Decks []store.Deck
	Query string
}

// deckGrid lists the user's decks, narrowed by the ?q= search term. The
// reference filters client-side on name and topic name; matching that
// server-side keeps the behaviour identical without shipping any JS.
func (s *Server) deckGrid(r *http.Request) (deckGridData, error) {
	decks, err := s.store.ListDecks(r.Context(), userFrom(r).ID, nil)
	if err != nil {
		return deckGridData{}, err
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q != "" {
		needle := strings.ToLower(q)
		filtered := decks[:0]
		for _, d := range decks {
			if strings.Contains(strings.ToLower(d.Name), needle) ||
				strings.Contains(strings.ToLower(d.TopicName), needle) {
				filtered = append(filtered, d)
			}
		}
		decks = filtered
	}
	return deckGridData{Decks: decks, Query: q}, nil
}

func (s *Server) handleDecksList(w http.ResponseWriter, r *http.Request) {
	grid, err := s.deckGrid(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "decks_list", grid)
}

func (s *Server) handleDecksGridFragment(w http.ResponseWriter, r *http.Request) {
	grid, err := s.deckGrid(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Push the page URL for the state actually rendered (see card_table).
	push := "/decks"
	if grid.Query != "" {
		push += "?q=" + url.QueryEscape(grid.Query)
	}
	w.Header().Set("HX-Push-Url", push)
	s.fragment(w, http.StatusOK, "deck_grid", grid)
}

func (s *Server) handleDeckNewPage(w http.ResponseWriter, r *http.Request) {
	topics, err := s.store.ListTopics(r.Context(), userFrom(r).ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// ?topicId= preselects the topic.
	params := store.DeckParams{}
	if tid := queryInt64Ptr(r, "topicId"); tid != nil {
		params.TopicID = *tid
	}
	s.render(w, r, http.StatusOK, "decks_form", deckFormData{Topics: topics, Params: params})
}

func (s *Server) handleDeckCreate(w http.ResponseWriter, r *http.Request) {
	userID := userFrom(r).ID
	p := deckParamsFromForm(r)
	renderErr := func(status int, msg string) {
		topics, err := s.store.ListTopics(r.Context(), userID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.render(w, r, status, "decks_form", deckFormData{Topics: topics, Params: p, Error: msg})
	}
	if p.Name == "" || p.TopicID == 0 {
		renderErr(http.StatusBadRequest, "Name and topic are required.")
		return
	}
	deck, err := s.store.CreateDeck(r.Context(), userID, p)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			renderErr(http.StatusConflict, "A deck with this name already exists in this topic.")
			return
		}
		s.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/decks/"+itoa(deck.ID), http.StatusSeeOther)
}

// deckShowData feeds decks_show.html: deck header + the first render of the
// card table fragment.
type deckShowData struct {
	Deck  store.Deck
	Table cardTableData
}

func (s *Server) handleDeckShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	userID := userFrom(r).ID
	deck, err := s.store.GetDeck(r.Context(), userID, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	table, err := s.cardTable(r, &id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "decks_show", deckShowData{Deck: deck, Table: table})
}

func (s *Server) handleDeckEditPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	deck, err := s.store.GetDeck(r.Context(), userFrom(r).ID, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "decks_form", deckFormData{Deck: &deck})
}

func (s *Server) handleDeckUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	userID := userFrom(r).ID
	deck, err := s.store.GetDeck(r.Context(), userID, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	p := deckParamsFromForm(r)
	p.TopicID = deck.TopicID // deck moves between topics are not supported in the UI
	if p.Name == "" {
		s.render(w, r, http.StatusBadRequest, "decks_form", deckFormData{Deck: &deck, Params: p, Error: "Name is required."})
		return
	}
	if _, err := s.store.UpdateDeck(r.Context(), userID, id, p); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			s.render(w, r, http.StatusConflict, "decks_form", deckFormData{Deck: &deck, Params: p, Error: "A deck with this name already exists in this topic."})
			return
		}
		s.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/decks/"+itoa(id), http.StatusSeeOther)
}

func (s *Server) handleDeckDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteDeck(r.Context(), userFrom(r).ID, id); err != nil {
		s.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/decks", http.StatusSeeOther)
}

// handleDeckExport streams the deck as YAML (port of the export endpoint,
// same filename sanitizer and metadata).
// handleDeckExportPreview serves the same YAML as the export endpoint but
// without the attachment header, so the share dialog can hx-get it into a
// readonly textarea. The reference fetches it the same way; doing it on demand
// avoids serialising every card twice on each deck-detail page load.
func (s *Server) handleDeckExportPreview(w http.ResponseWriter, r *http.Request) {
	content, _, err := s.deckExportYAML(r)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(content))
}

func (s *Server) handleDeckExport(w http.ResponseWriter, r *http.Request) {
	content, deckName, err := s.deckExportYAML(r)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	filename := sanitizeFilename(deckName) + "_cards.yaml"
	w.Header().Set("Content-Type", "text/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Write([]byte(content))
}

// deckExportYAML builds a deck's YAML export, honouring ?includeSm2Params.
// Shared by the download and preview handlers.
func (s *Server) deckExportYAML(r *http.Request) (string, string, error) {
	id, ok := pathID(r, "id")
	if !ok {
		return "", "", store.ErrNotFound
	}
	user := userFrom(r)
	includeSM2 := r.URL.Query().Get("includeSm2Params") == "true"

	deck, err := s.store.GetDeck(r.Context(), user.ID, id)
	if err != nil {
		return "", "", err
	}
	cards, _, err := s.store.ListCards(r.Context(), user.ID, store.CardListParams{DeckID: &id})
	if err != nil {
		return "", "", err
	}

	exports := make([]yamlio.ExportCard, len(cards))
	for i, c := range cards {
		ec := yamlio.ExportCard{
			Front: c.Front, Back: c.Back, Note: c.Note, Priority: c.Priority, Tags: c.Tags,
		}
		if fwd := c.ForwardSchedule(); fwd != nil {
			ec.Forward = fwd.State()
		}
		// Only for currently-bidirectional decks: a dormant reverse schedule
		// (left behind when bidirectionality was switched off) must not leak
		// Reverse* fields, which would flip the target deck to bidirectional
		// on re-import.
		if rev := c.ReverseSchedule(); rev != nil && deck.IsBidirectional {
			st := rev.State()
			ec.Reverse = &st
		}
		exports[i] = ec
	}

	meta := &yamlio.Metadata{
		FormatVersion: "1.0",
		DeckName:      deck.Name,
		CreatorName:   user.Name,
		ExportDate:    time.Now().UTC().Format("2006-01-02"),
		CardCount:     len(cards),
	}
	content, err := yamlio.Export(exports, meta, includeSM2)
	if err != nil {
		return "", "", err
	}
	return content, deck.Name, nil
}

// sanitizeFilename ports `name.replace(/[^a-z0-9]/gi, '_').toLowerCase()`.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
