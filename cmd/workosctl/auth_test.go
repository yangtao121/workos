package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadTerminalLineLeavesNextPromptBytesUnread(t *testing.T) {
	input := bytes.NewBufferString("owner\r\nnext prompt bytes\n")
	username, err := readTerminalLine(input, 512)
	if err != nil || username != "owner" {
		t.Fatalf("username read: %v", err)
	}
	if got := input.String(); got != "next prompt bytes\n" {
		t.Fatal("username reader consumed bytes intended for next prompt")
	}
	if _, err := readTerminalLine(strings.NewReader(strings.Repeat("u", 513)), 512); err == nil {
		t.Fatal("unbounded terminal line accepted")
	}
}
