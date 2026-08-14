package store

import (
	"errors"
	"testing"
	"time"
)

func TestConversationLifecycle(t *testing.T) {
	s := newTestStore(t)
	userID, topicID, deckID := seed(t, s, false)
	card := mkCard(t, s, userID, deckID, "hola", "A")
	schedID := card.ForwardSchedule().ID

	convID, err := s.CreateConversation(ctx, userID, topicID, &schedID,
		[]ChatMessage{{Role: "assistant", Content: "What does hola mean?"}})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := s.AppendChatMessages(ctx, userID, convID, []ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "Correct!"},
	}); err != nil {
		t.Fatalf("AppendChatMessages: %v", err)
	}

	conv, msgs, err := s.GetConversationMessages(ctx, userID, convID)
	if err != nil {
		t.Fatalf("GetConversationMessages: %v", err)
	}
	if conv.TopicID != topicID || conv.ScheduleID == nil || *conv.ScheduleID != schedID {
		t.Errorf("conversation = %+v", conv)
	}
	if len(msgs) != 3 || msgs[0].Content != "What does hola mean?" || msgs[1].Role != "user" || msgs[2].Content != "Correct!" {
		t.Errorf("transcript = %+v", msgs)
	}

	latest, msgs, err := s.LatestConversationForSchedule(ctx, userID, schedID)
	if err != nil || latest.ID != convID || len(msgs) != 3 {
		t.Errorf("LatestConversationForSchedule = %+v (%d msgs), %v", latest, len(msgs), err)
	}

	// Topic chat: nil schedule.
	topicConv, err := s.CreateConversation(ctx, userID, topicID, nil, nil)
	if err != nil {
		t.Fatalf("topic CreateConversation: %v", err)
	}
	if conv, _, err := s.GetConversationMessages(ctx, userID, topicConv); err != nil || conv.ScheduleID != nil {
		t.Errorf("topic conversation = %+v, %v", conv, err)
	}
}

func TestConversationOwnershipScoping(t *testing.T) {
	s := newTestStore(t)
	userID, topicID, deckID := seed(t, s, false)
	card := mkCard(t, s, userID, deckID, "hola", "A")
	schedID := card.ForwardSchedule().ID
	convID, err := s.CreateConversation(ctx, userID, topicID, &schedID, nil)
	if err != nil {
		t.Fatal(err)
	}

	other, err := s.CreateUser(ctx, "Other", "other@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.GetConversationMessages(ctx, other.ID, convID); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign read = %v, want ErrNotFound", err)
	}
	if err := s.AppendChatMessages(ctx, other.ID, convID, []ChatMessage{{Role: "user", Content: "x"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign append = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateConversation(ctx, other.ID, topicID, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("create on foreign topic = %v, want ErrNotFound", err)
	}
	// A schedule that doesn't belong to the named topic inserts nothing.
	otherTopic, err := s.CreateTopic(ctx, userID, TopicParams{Name: "French"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateConversation(ctx, userID, otherTopic.ID, &schedID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("create with mismatched topic/schedule = %v, want ErrNotFound", err)
	}
}

func TestConversationCascadeAndTTL(t *testing.T) {
	s := newTestStore(t)
	userID, topicID, deckID := seed(t, s, false)
	card := mkCard(t, s, userID, deckID, "hola", "A")
	schedID := card.ForwardSchedule().ID

	convID, err := s.CreateConversation(ctx, userID, topicID, &schedID,
		[]ChatMessage{{Role: "assistant", Content: "q"}})
	if err != nil {
		t.Fatal(err)
	}
	// Deleting the card cascades schedule -> conversation -> messages.
	if err := s.DeleteCard(ctx, userID, card.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetConversationMessages(ctx, userID, convID); !errors.Is(err, ErrNotFound) {
		t.Errorf("conversation survived card delete: %v", err)
	}

	topicConv, err := s.CreateConversation(ctx, userID, topicID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A cutoff before every updated_at prunes nothing; one after prunes all.
	if n, err := s.DeleteStaleConversations(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Errorf("early cutoff pruned %d, %v", n, err)
	}
	if n, err := s.DeleteStaleConversations(ctx, time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Errorf("late cutoff pruned %d, %v", n, err)
	}
	if _, _, err := s.GetConversationMessages(ctx, userID, topicConv); !errors.Is(err, ErrNotFound) {
		t.Errorf("conversation survived TTL cleanup: %v", err)
	}
}
