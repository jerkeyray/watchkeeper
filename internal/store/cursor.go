package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

type operationCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func encodeCursor(value any) string {
	raw, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeOperationCursor(value string) (operationCursor, error) {
	if value == "" {
		return operationCursor{CreatedAt: time.Unix(0, 0).UTC(), ID: "00000000-0000-0000-0000-000000000000"}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return operationCursor{}, fmt.Errorf("decode cursor: %w", err)
	}
	var cursor operationCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return operationCursor{}, fmt.Errorf("parse cursor: %w", err)
	}
	if cursor.ID == "" || cursor.CreatedAt.IsZero() {
		return operationCursor{}, fmt.Errorf("cursor fields are required")
	}
	return cursor, nil
}

func encodeEventCursor(id int64) string {
	return encodeCursor(struct {
		ID int64 `json:"id"`
	}{id})
}

func decodeEventCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, fmt.Errorf("decode cursor: %w", err)
	}
	var cursor struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.ID < 0 {
		return 0, fmt.Errorf("invalid event cursor")
	}
	return cursor.ID, nil
}
