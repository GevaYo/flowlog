package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testCreds() exportedCredentials {
	return exportedCredentials{
		AccessKeyId:     "NEWKEY",
		SecretAccessKey: "NEWSECRET",
		SessionToken:    "NEWTOKEN",
	}
}

// profileHeader is derived from awsProfile so these tests keep passing when
// the placeholder profile name is changed to a real one.
var profileHeader = "[" + awsProfile + "]"

func newSection() string {
	return profileHeader + "\naws_access_key_id=NEWKEY\naws_secret_access_key=NEWSECRET\naws_session_token=NEWTOKEN"
}

func TestRewriteAwsCredentialsEmptyFile(t *testing.T) {
	got := rewriteAwsCredentials("", testCreds())
	want := newSection() + "\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteAwsCredentialsNoExistingSection(t *testing.T) {
	content := "[other]\naws_access_key_id=X\naws_secret_access_key=Y\naws_session_token=Z\n"
	got := rewriteAwsCredentials(content, testCreds())
	want := "[other]\naws_access_key_id=X\naws_secret_access_key=Y\naws_session_token=Z\n\n" + newSection() + "\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteAwsCredentialsSectionIsOnlyContent(t *testing.T) {
	content := profileHeader + "\naws_access_key_id=OLD\naws_secret_access_key=OLDSEC\naws_session_token=OLDTOK\n"
	got := rewriteAwsCredentials(content, testCreds())
	want := newSection() + "\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteAwsCredentialsSectionAtEnd(t *testing.T) {
	content := "[a]\nx=1\n\n" + profileHeader + "\naws_access_key_id=OLD\naws_secret_access_key=OLDSEC\naws_session_token=OLDTOK\n"
	got := rewriteAwsCredentials(content, testCreds())
	want := "[a]\nx=1\n\n" + newSection() + "\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteAwsCredentialsSectionInMiddle(t *testing.T) {
	content := "[a]\nx=1\n\n" + profileHeader + "\naws_access_key_id=OLD\naws_secret_access_key=OLDSEC\naws_session_token=OLDTOK\n\n[b]\ny=2\n"
	got := rewriteAwsCredentials(content, testCreds())

	if strings.Contains(got, "OLD") {
		t.Fatalf("old credentials not removed: %q", got)
	}
	if strings.Count(got, profileHeader) != 1 {
		t.Fatalf("expected exactly one %s section, got %q", profileHeader, got)
	}
	if !strings.Contains(got, "[a]\nx=1") {
		t.Fatalf("expected [a] section preserved, got %q", got)
	}
	if !strings.Contains(got, "[b]\ny=2") {
		t.Fatalf("expected [b] section preserved, got %q", got)
	}
	if !strings.HasSuffix(got, newSection()+"\n") {
		t.Fatalf("expected fresh section appended at end, got %q", got)
	}
	if strings.Index(got, "[a]") > strings.Index(got, "[b]") || strings.Index(got, "[b]") > strings.Index(got, profileHeader) {
		t.Fatalf("expected section order [a], [b], %s, got %q", profileHeader, got)
	}
}

func TestWriteAwsCredentialsFileAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".aws", "credentials")

	if err := writeAwsCredentialsFileAt(path, testCreds()); err != nil {
		t.Fatalf("writeAwsCredentialsFileAt: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := newSection() + "\n"
	if string(data) != want {
		t.Fatalf("got %q, want %q", string(data), want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("got mode %o, want 0600", perm)
	}

	// Overwriting an existing section should replace it, not duplicate it.
	if err := writeAwsCredentialsFileAt(path, exportedCredentials{
		AccessKeyId:     "SECONDKEY",
		SecretAccessKey: "SECONDSECRET",
		SessionToken:    "SECONDTOKEN",
	}); err != nil {
		t.Fatalf("writeAwsCredentialsFileAt (second write): %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile (second write): %v", err)
	}
	if strings.Contains(string(data), "NEWKEY") {
		t.Fatalf("expected first section to be replaced, got %q", string(data))
	}
	if strings.Count(string(data), profileHeader) != 1 {
		t.Fatalf("expected exactly one %s section, got %q", profileHeader, string(data))
	}
}
