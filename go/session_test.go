package mantyx

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSession_MultiTurn(t *testing.T) {
	server := newMockServer()
	defer server.close()
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})

	session, err := client.CreateSession(context.Background(), SessionSpec{SystemPrompt: "be helpful"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	out1, err := session.Send(context.Background(), "first")
	if err != nil {
		t.Fatalf("Send #1: %v", err)
	}
	if out1.Text != "echo:first" {
		t.Fatalf("unexpected reply 1: %q", out1.Text)
	}
	out2, err := session.Send(context.Background(), "second")
	if err != nil {
		t.Fatalf("Send #2: %v", err)
	}
	if out2.Text != "echo:second" {
		t.Fatalf("unexpected reply 2: %q", out2.Text)
	}
	hist, err := session.History(context.Background())
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 4 {
		t.Fatalf("expected 4 history entries, got %d (%v)", len(hist), hist)
	}
	if err := session.End(context.Background()); err != nil {
		t.Fatalf("End: %v", err)
	}
}

func TestSession_MetadataForwarded(t *testing.T) {
	server := newMockServer()
	defer server.close()
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})

	session, err := client.CreateSession(context.Background(), SessionSpec{
		SystemPrompt: "be helpful",
		Metadata:     map[string]string{"customer": "acme", "env": "prod"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var createBody map[string]any
	if err := json.Unmarshal(server.lastSessionCreateBody, &createBody); err != nil {
		t.Fatalf("parse create body: %v", err)
	}
	meta, _ := createBody["metadata"].(map[string]any)
	if meta["customer"] != "acme" || meta["env"] != "prod" {
		t.Fatalf("create metadata not forwarded: %#v", createBody["metadata"])
	}

	if _, err := session.Send(context.Background(), "hi", WithMetadata(map[string]string{"trace_id": "trace_1"})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var msgBody map[string]any
	if err := json.Unmarshal(server.lastSessionMessageBody, &msgBody); err != nil {
		t.Fatalf("parse message body: %v", err)
	}
	msgMeta, _ := msgBody["metadata"].(map[string]any)
	if msgMeta["trace_id"] != "trace_1" {
		t.Fatalf("send metadata not forwarded: %#v", msgBody["metadata"])
	}
}
