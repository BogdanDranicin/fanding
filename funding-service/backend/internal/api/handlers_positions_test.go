package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/funding-service/backend/internal/api"
	"github.com/rs/zerolog"
)

type mockRefresher struct{ token string }

func (m *mockRefresher) Token() string { return m.token }

type mockFetcher struct {
	positions []api.PositionJSON
	err       error
}

func (m *mockFetcher) GetPositions(token string) ([]api.PositionJSON, error) {
	return m.positions, m.err
}

type mockBrokerStore struct {
	saved *api.BrokerSettingsRequest
}

func (m *mockBrokerStore) UpsertBrokerConnection(sso, device, expiresAt string) error {
	m.saved = &api.BrokerSettingsRequest{SSOSession: sso, DeviceID: device}
	return nil
}
func (m *mockBrokerStore) GetBrokerConnection() *api.BrokerConnectionStatus { return nil }

func TestHandleGetPositions_NotConfigured(t *testing.T) {
	refresher := &mockRefresher{token: ""}
	h := api.HandleGetPositions(refresher, nil, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/positions", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", w.Code)
	}
}

func TestHandleGetPositions_Success(t *testing.T) {
	refresher := &mockRefresher{token: "valid-token"}
	fetcher := &mockFetcher{positions: []api.PositionJSON{{Symbol: "VTBR", Side: "buy"}}}
	h := api.HandleGetPositions(refresher, fetcher, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/positions", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	var pos []api.PositionJSON
	if err := json.NewDecoder(w.Body).Decode(&pos); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(pos) != 1 || pos[0].Symbol != "VTBR" {
		t.Errorf("unexpected positions: %+v", pos)
	}
}

func TestHandlePostSettingsPositions_SavesData(t *testing.T) {
	store := &mockBrokerStore{}
	h := api.HandlePostSettingsPositions(store)

	body := `{"sso_session":"my-sso","device_id":"my-device"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/positions",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if store.saved == nil {
		t.Fatal("expected store.UpsertBrokerConnection to be called")
	}
	if store.saved.SSOSession != "my-sso" {
		t.Errorf("want my-sso, got %s", store.saved.SSOSession)
	}
}

func TestHandlePostSettingsPositions_MissingFields(t *testing.T) {
	store := &mockBrokerStore{}
	h := api.HandlePostSettingsPositions(store)

	body := `{"sso_session":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/positions",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}
