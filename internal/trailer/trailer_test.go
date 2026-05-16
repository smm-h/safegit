package trailer

import (
	"os"
	"strings"
	"testing"
)

func TestInject_EnvAbsent(t *testing.T) {
	os.Unsetenv(envVar)
	msg := "some commit message"
	got := Inject(msg)
	if got != msg {
		t.Errorf("expected message unchanged, got %q", got)
	}
}

func TestInject_EnvPresent(t *testing.T) {
	t.Setenv(envVar, "session-abc-123")
	msg := "add new feature"
	got := Inject(msg)

	if !strings.Contains(got, "Claude-Code-Session-Id: session-abc-123") {
		t.Errorf("expected trailer in output, got %q", got)
	}

	// Should have blank line separator between subject and trailer
	if !strings.Contains(got, "\n\nClaude-Code-Session-Id: session-abc-123\n") {
		t.Errorf("expected blank line before trailer, got %q", got)
	}
}

func TestInject_Dedup_SameSession(t *testing.T) {
	t.Setenv(envVar, "session-abc-123")
	msg := "add new feature\n\nClaude-Code-Session-Id: session-abc-123\n"
	got := Inject(msg)
	if got != msg {
		t.Errorf("expected message unchanged (dedup), got %q", got)
	}
}

func TestInject_DifferentSession_BothPresent(t *testing.T) {
	t.Setenv(envVar, "session-def-456")
	msg := "add new feature\n\nClaude-Code-Session-Id: session-abc-123\n"
	got := Inject(msg)

	if !strings.Contains(got, "Claude-Code-Session-Id: session-abc-123") {
		t.Errorf("expected original trailer preserved, got %q", got)
	}
	if !strings.Contains(got, "Claude-Code-Session-Id: session-def-456") {
		t.Errorf("expected new trailer added, got %q", got)
	}

	// Both trailers should be in the same block (no extra blank line between them)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	// Last two lines should be the two trailers
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d", len(lines))
	}
	last := lines[len(lines)-1]
	secondLast := lines[len(lines)-2]
	if !strings.HasPrefix(last, "Claude-Code-Session-Id:") || !strings.HasPrefix(secondLast, "Claude-Code-Session-Id:") {
		t.Errorf("expected last two lines to be trailers, got %q and %q", secondLast, last)
	}
}

func TestInject_MultiLineBody(t *testing.T) {
	t.Setenv(envVar, "session-xyz")
	msg := "subject line\n\nThis is the body.\nIt has multiple lines."
	got := Inject(msg)

	// Trailer should be after a blank line following the body
	expected := "subject line\n\nThis is the body.\nIt has multiple lines.\n\nClaude-Code-Session-Id: session-xyz\n"
	if got != expected {
		t.Errorf("expected:\n%q\ngot:\n%q", expected, got)
	}
}

func TestInject_ExistingOtherTrailers(t *testing.T) {
	t.Setenv(envVar, "session-xyz")
	msg := "subject line\n\nSigned-off-by: Test User <test@test.com>\nReviewed-by: Other <other@test.com>\n"
	got := Inject(msg)

	// New trailer should be appended in the same block (no extra blank line)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	last := lines[len(lines)-1]
	if last != "Claude-Code-Session-Id: session-xyz" {
		t.Errorf("expected last line to be session trailer, got %q", last)
	}

	// The line before should be Reviewed-by (existing trailer block)
	secondLast := lines[len(lines)-2]
	if !strings.HasPrefix(secondLast, "Reviewed-by:") {
		t.Errorf("expected second-to-last line to be existing trailer, got %q", secondLast)
	}

	// Should NOT have two blank lines (trailer appended to existing block)
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("should not have double blank lines, got %q", got)
	}
}

func TestInject_EmptyEnvVar(t *testing.T) {
	t.Setenv(envVar, "")
	msg := "some message"
	got := Inject(msg)
	if got != msg {
		t.Errorf("expected message unchanged for empty env var, got %q", got)
	}
}

func TestAppendCustom_EmptyTrailers(t *testing.T) {
	msg := "some commit message"
	got := AppendCustom(msg, nil)
	if got != msg {
		t.Errorf("expected message unchanged for nil trailers, got %q", got)
	}

	got = AppendCustom(msg, []string{})
	if got != msg {
		t.Errorf("expected message unchanged for empty trailers, got %q", got)
	}
}

func TestAppendCustom_OneTrailer(t *testing.T) {
	msg := "add new feature"
	got := AppendCustom(msg, []string{"Agent: test123"})

	expected := "add new feature\n\nAgent: test123\n"
	if got != expected {
		t.Errorf("expected:\n%q\ngot:\n%q", expected, got)
	}
}

func TestAppendCustom_MultipleTrailers(t *testing.T) {
	msg := "add new feature"
	got := AppendCustom(msg, []string{"Agent: test123", "Review: approved"})

	expected := "add new feature\n\nAgent: test123\nReview: approved\n"
	if got != expected {
		t.Errorf("expected:\n%q\ngot:\n%q", expected, got)
	}
}

func TestAppendCustom_ExistingTrailerBlock(t *testing.T) {
	msg := "subject line\n\nSigned-off-by: Test User <test@test.com>\n"
	got := AppendCustom(msg, []string{"Agent: test123"})

	// Should append directly to the existing trailer block (no extra blank line)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	last := lines[len(lines)-1]
	if last != "Agent: test123" {
		t.Errorf("expected last line to be custom trailer, got %q", last)
	}

	secondLast := lines[len(lines)-2]
	if !strings.HasPrefix(secondLast, "Signed-off-by:") {
		t.Errorf("expected second-to-last line to be existing trailer, got %q", secondLast)
	}

	// Should NOT have two blank lines
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("should not have double blank lines, got %q", got)
	}
}

func TestInject_TrailingNewlines(t *testing.T) {
	t.Setenv(envVar, "session-123")
	msg := "subject\n\n\n"
	got := Inject(msg)

	// Should trim trailing newlines, add blank line, trailer
	if !strings.Contains(got, "Claude-Code-Session-Id: session-123") {
		t.Errorf("expected trailer, got %q", got)
	}
	// Should not have excessive blank lines
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("should not have triple newlines, got %q", got)
	}
}
