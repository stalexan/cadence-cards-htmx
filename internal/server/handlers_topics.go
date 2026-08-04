package server

import (
	"errors"
	"net/http"

	"cadence-cards/internal/store"
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
