package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

func CanonicalJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("JSON value is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("multiple JSON values are not allowed")
	} else if err != io.EOF {
		return nil, fmt.Errorf("decode trailing JSON: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize JSON: %w", err)
	}
	return canonical, nil
}

func Fingerprint(targetService, action string, request json.RawMessage) (string, error) {
	canonical, err := CanonicalJSON(request)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		TargetService string          `json:"target_service"`
		Action        string          `json:"action"`
		Request       json.RawMessage `json:"request"`
	}{targetService, action, canonical})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func ReceiptFingerprint(receipt json.RawMessage) (string, error) {
	canonical, err := CanonicalJSON(receipt)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
