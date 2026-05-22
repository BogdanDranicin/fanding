package storage_test

import (
	"testing"
	"time"

	"github.com/funding-service/backend/internal/storage"
)

func TestBrokerConnectionTypes(t *testing.T) {
	conn := storage.BrokerConnection{
		SSOSession: "test-session",
		DeviceID:   "test-device",
		ExpiresAt:  time.Now().Add(30 * 24 * time.Hour),
	}
	if conn.SSOSession == "" {
		t.Fatal("SSOSession should not be empty")
	}
}
