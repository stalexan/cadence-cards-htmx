package server

import (
	"net/http"

	"cadence-cards/internal/claude"
	"cadence-cards/internal/store"
)

// topicConfig loads a topic (ownership-checked) and builds its prompt config.
func (s *Server) topicConfig(r *http.Request, topicID int64) (store.Topic, claude.TopicConfig, error) {
	topic, err := s.store.GetTopic(r.Context(), userFrom(r).ID, topicID)
	if err != nil {
		return store.Topic{}, claude.TopicConfig{}, err
	}
	cfg := claude.NewTopicConfig(topic.Name, topic.TopicDescription, topic.Expertise, topic.Focus,
		topic.ContextType, topic.Example, topic.Question)
	return topic, cfg, nil
}

// toClaudeMessages converts a stored transcript into the AI call's history.
func toClaudeMessages(msgs []store.ChatMessage) []claude.Message {
	out := make([]claude.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, claude.Message{Role: m.Role, Content: m.Content})
	}
	return out
}

// conversationFor loads the transcript referenced by the conversationId form
// value (0 = no conversation yet). The conversation must belong to the user,
// the URL topic, and — for study chat — the given schedule; anything else is a
// 404, exactly like any other missing-or-not-owned resource.
func (s *Server) conversationFor(w http.ResponseWriter, r *http.Request, topicID int64, scheduleID *int64) (convID int64, history []store.ChatMessage, ok bool) {
	convID = int64(formInt(r, "conversationId", 0))
	if convID == 0 {
		return 0, nil, true
	}
	conv, msgs, err := s.store.GetConversationMessages(r.Context(), userFrom(r).ID, convID)
	if err != nil {
		s.storeError(w, r, err)
		return 0, nil, false
	}
	if conv.TopicID != topicID ||
		(scheduleID == nil) != (conv.ScheduleID == nil) ||
		(scheduleID != nil && *conv.ScheduleID != *scheduleID) {
		http.NotFound(w, r)
		return 0, nil, false
	}
	return convID, msgs, true
}

func (s *Server) handleChatIndex(w http.ResponseWriter, r *http.Request) {
	topics, err := s.store.ListTopics(r.Context(), userFrom(r).ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "chat_index", topics)
}

// chatComposerData feeds the shared chat_composer partial. ScheduleID 0 =
// topic chat (no hidden schedule input).
type chatComposerData struct {
	PostURL        string
	Placeholder    string
	ConversationID int64
	ScheduleID     int64
}

// chatShowData feeds chat_show.html.
type chatShowData struct {
	Topic    store.Topic
	Composer chatComposerData
}

func (s *Server) handleChatShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "topicId")
	if !ok {
		http.NotFound(w, r)
		return
	}
	topic, _, err := s.topicConfig(r, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "chat_show", chatShowData{
		Topic: topic,
		Composer: chatComposerData{
			PostURL:     "/chat/" + itoa(id) + "/message",
			Placeholder: "Ask about " + topic.Name + "…",
		},
	})
}

// chatExchangeData feeds the chat_messages.html fragment: the user's bubble,
// Claude's reply, and an OOB update of the hidden conversation-ID input.
type chatExchangeData struct {
	UserMessage    string
	Assistant      string
	IsError        bool
	ConversationID int64
}

func (s *Server) handleChatMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "topicId")
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, cfg, err := s.topicConfig(r, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	message := formStr(r, "message")
	if message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}
	convID, history, ok := s.conversationFor(w, r, id, nil)
	if !ok {
		return
	}
	isFirst := len(history) == 0

	reply, err := s.ai.ChatAboutTopic(r.Context(), cfg, message, toClaudeMessages(history), isFirst)
	data := chatExchangeData{UserMessage: message, ConversationID: convID}
	if err != nil {
		// Keep the conversation alive with an error bubble (matches the
		// Svelte chat's failure handling), with distinct copy per failure
		// class. A failed exchange leaves the stored transcript untouched.
		data.Assistant = aiErrorMessage(err)
		data.IsError = true
		s.fragment(w, http.StatusOK, "chat_exchange", data)
		return
	}

	userID := userFrom(r).ID
	exchange := []store.ChatMessage{
		{Role: "user", Content: message},
		{Role: "assistant", Content: reply},
	}
	if convID == 0 {
		convID, err = s.store.CreateConversation(r.Context(), userID, id, nil, exchange)
	} else {
		err = s.store.AppendChatMessages(r.Context(), userID, convID, exchange)
	}
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	data.Assistant = reply
	data.ConversationID = convID
	s.fragment(w, http.StatusOK, "chat_exchange", data)
}
