package agent

import (
	"testing"
	"time"
)

func TestSessionManager_GetOrCreate(t *testing.T) {
	sm := NewSessionManager()

	session := sm.GetOrCreate("player1", "tenant1")
	if session.PlayerID != "player1" || session.TenantID != "tenant1" {
		t.Errorf("GetOrCreate() = %+v, want player1/tenant1", session)
	}
	if session.ID == "" {
		t.Error("session.ID should not be empty")
	}
	if !session.IsFirstVisit {
		t.Error("session.IsFirstVisit should be true for new session")
	}

	session2 := sm.GetOrCreate("player1", "tenant1")
	if session2.ID != session.ID {
		t.Errorf("GetOrCreate() returned different session for same player/tenant, got %s, want %s", session2.ID, session.ID)
	}
}

func TestSessionManager_Get(t *testing.T) {
	sm := NewSessionManager()

	session := sm.GetOrCreate("player1", "tenant1")

	got, ok := sm.Get(session.ID)
	if !ok {
		t.Fatal("Get() returned false, want true")
	}
	if got.ID != session.ID {
		t.Errorf("Get() = %+v, want ID %s", got, session.ID)
	}

	_, ok = sm.Get("non-existent")
	if ok {
		t.Error("Get() returned true for non-existent session, want false")
	}
}

func TestSessionManager_Remove(t *testing.T) {
	sm := NewSessionManager()

	session := sm.GetOrCreate("player1", "tenant1")
	sm.Remove(session.ID)

	_, ok := sm.Get(session.ID)
	if ok {
		t.Error("Get() returned true after Remove(), want false")
	}
}

func TestSession_AddMessage(t *testing.T) {
	sm := NewSessionManager()
	session := sm.GetOrCreate("player1", "tenant1")

	session.AddMessage("user", "hello", "happy", nil)
	session.AddMessage("assistant", "hi", "neutral", nil)

	msgs := session.GetMessages(10)
	if len(msgs) != 2 {
		t.Fatalf("len(GetMessages()) = %d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("first message = %+v, want user/hello", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hi" {
		t.Errorf("second message = %+v, want assistant/hi", msgs[1])
	}
}

func TestSession_GetMessages_Limit(t *testing.T) {
	sm := NewSessionManager()
	session := sm.GetOrCreate("player1", "tenant1")

	for i := 0; i < 5; i++ {
		session.AddMessage("user", "msg"+string(rune('a'+i)), "neutral", nil)
	}

	msgs := session.GetMessages(3)
	if len(msgs) != 3 {
		t.Fatalf("len(GetMessages(3)) = %d, want 3", len(msgs))
	}
	if msgs[0].Content != "msgc" {
		t.Errorf("first message = %q, want %q", msgs[0].Content, "msgc")
	}
}

func TestSession_GetMessages_All(t *testing.T) {
	sm := NewSessionManager()
	session := sm.GetOrCreate("player1", "tenant1")

	for i := 0; i < 3; i++ {
		session.AddMessage("user", "msg"+string(rune('a'+i)), "neutral", nil)
	}

	msgs := session.GetMessages(0)
	if len(msgs) != 3 {
		t.Fatalf("len(GetMessages(0)) = %d, want 3", len(msgs))
	}
}

func TestSession_MarkVisited(t *testing.T) {
	sm := NewSessionManager()
	session := sm.GetOrCreate("player1", "tenant1")

	if !session.IsFirstVisit {
		t.Error("IsFirstVisit should be true initially")
	}

	session.MarkVisited()

	if session.IsFirstVisit {
		t.Error("IsFirstVisit should be false after MarkVisited()")
	}
}

func TestSession_UpdateNickname(t *testing.T) {
	sm := NewSessionManager()
	session := sm.GetOrCreate("player1", "tenant1")

	if session.Nickname != "玩家" {
		t.Errorf("Nickname = %q, want %q", session.Nickname, "玩家")
	}

	session.UpdateNickname("Alice")

	if session.Nickname != "Alice" {
		t.Errorf("Nickname = %q, want %q", session.Nickname, "Alice")
	}
}

func TestSession_LastActive(t *testing.T) {
	sm := NewSessionManager()
	session := sm.GetOrCreate("player1", "tenant1")

	initialActive := session.LastActive
	time.Sleep(10 * time.Millisecond)

	session.AddMessage("user", "test", "neutral", nil)

	if session.LastActive.Before(initialActive) || session.LastActive.Equal(initialActive) {
		t.Error("LastActive should be updated after AddMessage")
	}
}