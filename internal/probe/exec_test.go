package probe

import "testing"

func TestExecEventCommand(t *testing.T) {
	event := ExecEvent{Comm: [taskCommLen]byte{'s', 'l', 'e', 'e', 'p'}}
	if got, want := event.Command(), "sleep"; got != want {
		t.Fatalf("Command() = %q, want %q", got, want)
	}
}

func TestExecEventCommandWithoutNullTerminator(t *testing.T) {
	event := ExecEvent{Comm: [taskCommLen]byte{'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p'}}
	if got, want := event.Command(), "abcdefghijklmnop"; got != want {
		t.Fatalf("Command() = %q, want %q", got, want)
	}
}
