package domain

import (
	"encoding/json"
	"testing"
)

func TestFingerprintCanonicalizesObjectKeys(t *testing.T) {
	a, err := Fingerprint("email", "send", json.RawMessage(`{"recipient":"a","meta":{"b":2,"a":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Fingerprint("email", "send", json.RawMessage(` { "meta": {"a":1,"b":2}, "recipient":"a" } `))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("fingerprints differ: %s %s", a, b)
	}
}
func TestFingerprintIncludesTargetAndAction(t *testing.T) {
	raw := json.RawMessage(`{"x":1}`)
	a, _ := Fingerprint("email", "send", raw)
	b, _ := Fingerprint("calendar", "send", raw)
	c, _ := Fingerprint("email", "cancel", raw)
	if a == b || a == c {
		t.Fatal("target and action must affect fingerprint")
	}
}
func TestCanonicalJSONRejectsMultipleValues(t *testing.T) {
	if _, err := CanonicalJSON(json.RawMessage(`{} {}`)); err == nil {
		t.Fatal("expected error")
	}
}
func TestReceiptFingerprintCanonical(t *testing.T) {
	a, _ := ReceiptFingerprint(json.RawMessage(`{"status":"committed","id":1}`))
	b, _ := ReceiptFingerprint(json.RawMessage(`{"id":1,"status":"committed"}`))
	if a != b {
		t.Fatal("receipt fingerprint must be canonical")
	}
}
