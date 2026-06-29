package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// fakeDeviceDriver is a controllable AuthDriver that also implements the
// device-code polling capability. It records polls and returns a canned result,
// so the admin poll endpoint can be exercised without a real upstream.
type fakeDeviceDriver struct {
	name   string
	polls  int
	result auth.SessionStatusResult
}

func (f *fakeDeviceDriver) Name() string            { return f.name }
func (f *fakeDeviceDriver) Scheme() auth.AuthScheme { return auth.SchemeOAuthDeviceCode }
func (f *fakeDeviceDriver) Start(_ context.Context, _ string) (auth.StartResult, error) {
	return auth.StartResult{DeviceCode: "fake-device-code"}, nil
}
func (f *fakeDeviceDriver) Exchange(_ context.Context, _ string, _, _, _ string) (storage.OAuthCredential, error) {
	return storage.OAuthCredential{}, nil
}
func (f *fakeDeviceDriver) Refresh(_ context.Context, c storage.OAuthCredential) (storage.OAuthCredential, error) {
	return c, nil
}
func (f *fakeDeviceDriver) PollWithDeviceCode(_ context.Context, _ string) (auth.SessionStatusResult, error) {
	f.polls++
	return f.result, nil
}

// TestOAuthDevicePolling asserts the GET /sessions/:id endpoint drives a
// device-code session by calling the driver's PollWithDeviceCode and flips the
// session to complete when the upstream authorizes. The prior code returned a
// hard-coded "pending" (cutover blocker B4).
func TestOAuthDevicePolling(t *testing.T) {
	st := memory.New()
	reg := auth.NewRegistry()
	fake := &fakeDeviceDriver{
		name: "fake",
		result: auth.SessionStatusResult{
			Status:     auth.StatusComplete,
			Credential: &storage.OAuthCredential{DriverKey: "fake", AccessToken: "at", Scheme: string(auth.SchemeOAuthDeviceCode)},
		},
	}
	reg.Register("fake", fake)
	sessions := auth.NewSessionStore()

	r := chi.NewRouter()
	MountOAuth(r, st.Storage(), reg, sessions)

	// Start a device-code session.
	rec := do(r, "POST", "/api/v1/auth/sessions", "", []byte(`{"driver":"fake"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("start session → %d %s", rec.Code, rec.Body.String())
	}
	var startResp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &startResp); err != nil {
		t.Fatal(err)
	}
	if startResp.SessionID == "" {
		t.Fatal("no session_id in start response")
	}

	// Poll — admin must invoke PollWithDeviceCode and flip status to complete.
	rec = do(r, "GET", "/api/v1/auth/sessions/"+startResp.SessionID, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll → %d %s", rec.Code, rec.Body.String())
	}
	if fake.polls == 0 {
		t.Error("poll endpoint did not invoke PollWithDeviceCode on the driver")
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"status":"complete"`)) {
		t.Errorf("poll status → %s; want complete", rec.Body.String())
	}
}
