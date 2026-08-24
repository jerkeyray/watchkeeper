package store

import (
	"testing"
	"time"
)

func TestOperationCursorRoundTrip(t *testing.T) {
	want := operationCursor{CreatedAt: time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC), ID: "018f22d8-1951-7c8b-9f6b-dac0d181a9ad"}
	got, err := decodeOperationCursor(encodeCursor(want))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
func TestInvalidCursors(t *testing.T) {
	if _, err := decodeOperationCursor("not-base64"); err == nil {
		t.Fatal("expected operation cursor error")
	}
	if _, err := decodeEventCursor(encodeCursor(struct {
		ID int64 `json:"id"`
	}{-1})); err == nil {
		t.Fatal("expected event cursor error")
	}
}
