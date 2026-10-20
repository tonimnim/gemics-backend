package httpapi

import (
	"strings"
	"testing"
)

func TestHideContactDetails(t *testing.T) {
	hidden := []string{
		"call 0712345678", "call +254 712 345 678", "call 0712-345-678", "text (0712) 345 678",
		"mail me at player.one@gmail.com", "go to https://example.com/x", "www.example.org",
		"wa.me/254712345678", "t.me/someone", "join chat.whatsapp.com/abc",
	}
	for _, text := range hidden {
		got, changed := hideContactDetails(text)
		if !changed || !strings.Contains(got, "[hidden]") || strings.ContainsAny(got, "@") {
			t.Errorf("hideContactDetails(%q) = %q, %v", text, got, changed)
		}
	}
	kept := []string{"8pm works", "it was 2-1", "room 123456", "ABC 123", "gg, well played", "give me 10 min"}
	for _, text := range kept {
		if got, changed := hideContactDetails(text); changed || got != text {
			t.Errorf("hideContactDetails(%q) = %q, %v; want it unchanged", text, got, changed)
		}
	}
}

func TestCleanChatText(t *testing.T) {
	cases := map[string]string{
		"  hello  ":          "hello",
		"a\tb":               "a b",
		"line\n\n\n\n\nnext": "line\n\nnext",
		"bell\x07 gone":      "bell gone",
		"crlf\r\nok":         "crlf\nok",
	}
	for raw, want := range cases {
		if got, ok := cleanChatText(raw); !ok || got != want {
			t.Errorf("cleanChatText(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "   \n\t ", strings.Repeat("x", 501), "\xff"} {
		if _, ok := cleanChatText(raw); ok {
			t.Errorf("cleanChatText(%q) accepted", raw)
		}
	}
}

func TestMatchChatDraftNeedsExactlyItsKindsFields(t *testing.T) {
	accepted := true
	valid := []matchChatMessageInput{
		{ClientMessageID: "a", Kind: "text", Text: "hi"},
		{ClientMessageID: "a", Kind: "quick_reply", QuickReply: "ready_now"},
		{ClientMessageID: "a", Kind: "room_code", RoomCode: "AB-12 34"},
		{ClientMessageID: "a", Kind: "time_answer", ReplyToSeq: 3, Accepted: &accepted},
	}
	for _, input := range valid {
		if _, problem := input.draft(); problem != "" {
			t.Errorf("%+v refused: %s", input, problem)
		}
	}
	invalid := []matchChatMessageInput{
		{ClientMessageID: "a", Kind: "text"},
		{ClientMessageID: "a", Kind: "text", Text: "hi", RoomCode: "AB"},
		{ClientMessageID: "a", Kind: "quick_reply", QuickReply: "call_me"},
		{ClientMessageID: "a", Kind: "room_code", RoomCode: "-AB"},
		{ClientMessageID: "a", Kind: "time_proposal"},
		{ClientMessageID: "a", Kind: "time_answer", ReplyToSeq: 3},
		{ClientMessageID: "a b", Kind: "text", Text: "hi"},
		{ClientMessageID: "a", Kind: "sticker"},
	}
	for _, input := range invalid {
		if _, problem := input.draft(); problem == "" {
			t.Errorf("%+v accepted", input)
		}
	}
}
