package server

import (
	"errors"
	"net/http"
	"time"

	"cadence-cards/internal/store"
	"cadence-cards/internal/yamlio"
)

func topicParamsFromForm(r *http.Request) store.TopicParams {
	return store.TopicParams{
		Name:             formStr(r, "name"),
		TopicDescription: formStrPtr(r, "topicDescription"),
		Expertise:        formStrPtr(r, "expertise"),
		Focus:            formStrPtr(r, "focus"),
		ContextType:      formStrPtr(r, "contextType"),
		Example:          formStrPtr(r, "example"),
		Question:         formStrPtr(r, "question"),
	}
}

// topicFormData feeds topics_form.html for both create and edit.
type topicFormData struct {
	Topic  *store.Topic
	Params store.TopicParams
	Error  string
}

func (s *Server) handleTopicsList(w http.ResponseWriter, r *http.Request) {
	topics, err := s.store.ListTopics(r.Context(), userFrom(r).ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "topics_list", topics)
}

func (s *Server) handleTopicNewPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "topics_form", topicFormData{})
}

func (s *Server) handleTopicCreate(w http.ResponseWriter, r *http.Request) {
	p := topicParamsFromForm(r)
	if p.Name == "" {
		s.render(w, r, http.StatusBadRequest, "topics_form", topicFormData{Params: p, Error: "Name is required."})
		return
	}
	topic, err := s.store.CreateTopic(r.Context(), userFrom(r).ID, p)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			s.render(w, r, http.StatusConflict, "topics_form", topicFormData{Params: p, Error: "A topic with this name already exists."})
			return
		}
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/topics/"+itoa(topic.ID), http.StatusSeeOther)
}

// topicShowData feeds topics_show.html.
type topicShowData struct {
	Topic store.Topic
	Decks []store.Deck
}

func (s *Server) handleTopicShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	userID := userFrom(r).ID
	topic, err := s.store.GetTopic(r.Context(), userID, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	decks, err := s.store.ListDecks(r.Context(), userID, &id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "topics_show", topicShowData{Topic: topic, Decks: decks})
}

func (s *Server) handleTopicEditPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	topic, err := s.store.GetTopic(r.Context(), userFrom(r).ID, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "topics_form", topicFormData{Topic: &topic})
}

func (s *Server) handleTopicUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	userID := userFrom(r).ID
	topic, err := s.store.GetTopic(r.Context(), userID, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	p := topicParamsFromForm(r)
	if p.Name == "" {
		s.render(w, r, http.StatusBadRequest, "topics_form", topicFormData{Topic: &topic, Params: p, Error: "Name is required."})
		return
	}
	if _, err := s.store.UpdateTopic(r.Context(), userID, id, p); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			s.render(w, r, http.StatusConflict, "topics_form", topicFormData{Topic: &topic, Params: p, Error: "A topic with this name already exists."})
			return
		}
		s.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/topics/"+itoa(id), http.StatusSeeOther)
}

// handleTopicExport streams the topic as YAML, honouring ?includeDecks and
// ?includeSm2Params. Filename is _topic.yaml, distinct from a deck's
// _cards.yaml, so the two formats are told apart before they are opened.
func (s *Server) handleTopicExport(w http.ResponseWriter, r *http.Request) {
	content, topicName, err := s.topicExportYAML(r)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	filename := sanitizeFilename(topicName) + "_topic.yaml"
	w.Header().Set("Content-Type", "text/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Write([]byte(content))
}

// handleTopicExportPreview serves the same YAML without the attachment header
// so the export dialog can hx-get it into a readonly textarea.
func (s *Server) handleTopicExportPreview(w http.ResponseWriter, r *http.Request) {
	content, _, err := s.topicExportYAML(r)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(content))
}

// topicExportYAML builds a topic's YAML export. Shared by the download and
// preview handlers.
func (s *Server) topicExportYAML(r *http.Request) (string, string, error) {
	id, ok := pathID(r, "id")
	if !ok {
		return "", "", store.ErrNotFound
	}
	user := userFrom(r)
	includeDecks := r.URL.Query().Get("includeDecks") == "true"
	includeSM2 := r.URL.Query().Get("includeSm2Params") == "true"

	topic, err := s.store.GetTopic(r.Context(), user.ID, id)
	if err != nil {
		return "", "", err
	}

	cfg := yamlio.TopicConfig{
		Name:             topic.Name,
		TopicDescription: topic.TopicDescription,
		Expertise:        topic.Expertise,
		Focus:            topic.Focus,
		ContextType:      topic.ContextType,
		Example:          topic.Example,
		Question:         topic.Question,
	}

	var exportDecks []yamlio.ExportDeck
	cardCount := 0
	if includeDecks {
		decks, err := s.store.ListDecks(r.Context(), user.ID, &id)
		if err != nil {
			return "", "", err
		}
		// One query for the whole topic (PerPage 0 = unpaginated), grouped in
		// Go, rather than one ListCards per deck.
		cards, _, err := s.store.ListCards(r.Context(), user.ID, store.CardListParams{TopicID: &id})
		if err != nil {
			return "", "", err
		}
		byDeck := make(map[int64][]store.Card, len(decks))
		for _, c := range cards {
			byDeck[c.DeckID] = append(byDeck[c.DeckID], c)
		}

		exportDecks = make([]yamlio.ExportDeck, len(decks))
		for i, d := range decks {
			ed := yamlio.ExportDeck{
				Name:            d.Name,
				Field1Label:     d.Field1Label,
				Field2Label:     d.Field2Label,
				IsBidirectional: d.IsBidirectional,
				Cards:           make([]yamlio.ExportCard, 0, len(byDeck[d.ID])),
			}
			for _, c := range byDeck[d.ID] {
				ec := yamlio.ExportCard{
					Front: c.Front, Back: c.Back, Note: c.Note, Priority: c.Priority, Tags: c.Tags,
				}
				if fwd := c.ForwardSchedule(); fwd != nil {
					ec.Forward = fwd.State()
				}
				// Same guard as deckExportYAML: a dormant reverse schedule
				// must not leak Reverse* fields and flip the deck on re-import.
				if rev := c.ReverseSchedule(); rev != nil && d.IsBidirectional {
					st := rev.State()
					ec.Reverse = &st
				}
				ed.Cards = append(ed.Cards, ec)
			}
			cardCount += len(ed.Cards)
			exportDecks[i] = ed
		}
	}

	meta := &yamlio.TopicMetadata{
		FormatVersion: "1.0",
		TopicName:     topic.Name,
		CreatorName:   user.Name,
		ExportDate:    time.Now().UTC().Format("2006-01-02"),
		DeckCount:     len(exportDecks),
		CardCount:     cardCount,
	}
	content, err := yamlio.ExportTopic(cfg, exportDecks, meta, includeDecks, includeSM2)
	if err != nil {
		return "", "", err
	}
	return content, topic.Name, nil
}

func (s *Server) handleTopicDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteTopic(r.Context(), userFrom(r).ID, id); err != nil {
		s.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/topics", http.StatusSeeOther)
}
