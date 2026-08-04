package server

import (
	"errors"
	"net/http"
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

func (s *Server) handleDecksList(w http.ResponseWriter, r *http.Request) {
	decks, err := s.store.ListDecks(r.Context(), userFrom(r).ID, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "decks_list", decks)
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
func (s *Server) handleDeckExport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	user := userFrom(r)
	includeSM2 := r.URL.Query().Get("includeSm2Params") == "true"

	deck, err := s.store.GetDeck(r.Context(), user.ID, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	cards, _, err := s.store.ListCards(r.Context(), user.ID, store.CardListParams{DeckID: &id})
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	exports := make([]yamlio.ExportCard, len(cards))
	for i, c := range cards {
		ec := yamlio.ExportCard{
			Front: c.Front, Back: c.Back, Note: c.Note, Priority: c.Priority, Tags: c.Tags,
		}
		if fwd := c.ForwardSchedule(); fwd != nil {
			ec.Forward = fwd.State()
		}
		if rev := c.ReverseSchedule(); rev != nil {
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
		s.serverError(w, r, err)
		return
	}

	filename := sanitizeFilename(deck.Name) + "_cards.yaml"
	w.Header().Set("Content-Type", "text/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Write([]byte(content))
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
