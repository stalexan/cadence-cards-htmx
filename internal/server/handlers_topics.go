package server

import (
	"errors"
	"net/http"
	"time"

	"cadence-cards/internal/claude"
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

// topicParamsFromTopic reads a stored topic back into form values.
func topicParamsFromTopic(t store.Topic) store.TopicParams {
	return store.TopicParams{
		Name:             t.Name,
		TopicDescription: t.TopicDescription,
		Expertise:        t.Expertise,
		Focus:            t.Focus,
		ContextType:      t.ContextType,
		Example:          t.Example,
		Question:         t.Question,
	}
}

// topicFieldsData feeds the topic_form_fields partial — every input on the
// topic form, plus the "what are you studying?" box above them. It is a
// partial rather than page markup because POST /topics/suggest re-renders the
// identical block with Claude's proposal filled in, so the suggested and typed
// states cannot drift apart.
type topicFieldsData struct {
	Values store.TopicParams
	// Seed is the description the suggestion was asked for, echoed back so the
	// swap does not wipe what the user typed into it.
	Seed string
	// Notice and Error render above the fields after a suggestion attempt.
	Notice string
	Error  string
}

// Defaults exposes the prompt builder's fallbacks to the template, which shows
// them as placeholders: a blank input then reads as what the prompt will use,
// not as a hole.
func (d topicFieldsData) Defaults() claude.Defaults { return claude.PromptDefaults }

// ShowAdvanced opens the optional-settings disclosure whenever it holds
// something worth seeing — an existing topic's settings, values that survived a
// failed submit, or a fresh suggestion. A blank new-topic form stays collapsed,
// which is the whole point of hiding them.
func (d topicFieldsData) ShowAdvanced() bool {
	if d.Notice != "" || d.Error != "" {
		return true
	}
	for _, p := range []*string{
		d.Values.Expertise, d.Values.Focus, d.Values.ContextType,
		d.Values.Example, d.Values.Question,
	} {
		if p != nil && *p != "" {
			return true
		}
	}
	return false
}

// topicFormData feeds topics_form.html for both create and edit.
type topicFormData struct {
	Topic  *store.Topic
	Fields topicFieldsData
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
		s.render(w, r, http.StatusBadRequest, "topics_form",
			topicFormData{Fields: topicFieldsData{Values: p}, Error: "Name is required."})
		return
	}
	topic, err := s.store.CreateTopic(r.Context(), userFrom(r).ID, p)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			s.render(w, r, http.StatusConflict, "topics_form",
				topicFormData{Fields: topicFieldsData{Values: p}, Error: "A topic with this name already exists."})
			return
		}
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/topics/"+itoa(topic.ID), http.StatusSeeOther)
}

// fillTopicParams writes a suggestion into p's *blank* fields only, leaving
// anything already typed alone, and reports how many it filled.
//
// Filling blanks rather than overwriting is what makes the button safe to press
// at any time: on a half-typed new topic it completes the rest, and on an
// existing topic it fills settings that were never written without touching the
// ones that were. Replacing a value is then an explicit act — clear the field
// and suggest again.
func fillTopicParams(p *store.TopicParams, sug claude.TopicSuggestion) int {
	filled := 0
	if p.Name == "" && sug.Name != "" {
		p.Name = sug.Name
		filled++
	}
	for _, f := range []struct {
		dst **string
		val string
	}{
		{&p.TopicDescription, sug.TopicDesc},
		{&p.Expertise, sug.Expertise},
		{&p.Focus, sug.Focus},
		{&p.ContextType, sug.ContextType},
		{&p.Example, sug.Example},
		{&p.Question, sug.Question},
	} {
		if *f.dst == nil && f.val != "" {
			v := f.val
			*f.dst = &v
			filled++
		}
	}
	return filled
}

// handleTopicSuggest proposes a topic's prompt configuration from a one-line
// description and returns the form's field block with the blanks filled in.
//
// It re-renders the same partial the page does, so what comes back is the live
// form, not a preview the user has to copy: nothing is saved until they submit.
// Failures render as a bubble inside that block at HTTP 200 — a suggestion is
// an optional convenience, so a missing API key must leave a usable form rather
// than an error page.
func (s *Server) handleTopicSuggest(w http.ResponseWriter, r *http.Request) {
	fields := topicFieldsData{
		Values: topicParamsFromForm(r),
		Seed:   formStr(r, "seed"),
	}
	if fields.Seed == "" {
		fields.Error = "Say what you are studying first — a few words is enough."
		s.fragment(w, http.StatusOK, "topic_form_fields", fields)
		return
	}

	sug, err := s.ai.SuggestTopicConfig(r.Context(), fields.Seed)
	if err != nil {
		fields.Error = aiErrorMessage(err)
		s.fragment(w, http.StatusOK, "topic_form_fields", fields)
		return
	}

	if filled := fillTopicParams(&fields.Values, sug); filled > 0 {
		fields.Notice = plural(filled, "field", "fields") +
			" filled in. Edit anything below, then save."
	} else {
		fields.Notice = "Every field is already filled in. Clear one and suggest again to replace it."
	}
	s.fragment(w, http.StatusOK, "topic_form_fields", fields)
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
	s.render(w, r, http.StatusOK, "topics_form", topicFormData{
		Topic:  &topic,
		Fields: topicFieldsData{Values: topicParamsFromTopic(topic)},
	})
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
		s.render(w, r, http.StatusBadRequest, "topics_form",
			topicFormData{Topic: &topic, Fields: topicFieldsData{Values: p}, Error: "Name is required."})
		return
	}
	if _, err := s.store.UpdateTopic(r.Context(), userID, id, p); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			s.render(w, r, http.StatusConflict, "topics_form",
				topicFormData{Topic: &topic, Fields: topicFieldsData{Values: p}, Error: "A topic with this name already exists."})
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
				ed.Cards = append(ed.Cards, exportCard(c, d.IsBidirectional))
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
