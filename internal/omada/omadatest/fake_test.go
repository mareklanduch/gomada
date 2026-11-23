package omadatest_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/mareklanduch/gomada/internal/omada/omadatest"
)

// call makes one request to the fake and decodes the response envelope.
func call(t *testing.T, fake *omadatest.Controller, method, path, token, body string) (code int, result map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, fake.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "AccessToken="+token)
	}
	resp, err := fake.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env struct {
		ErrorCode int            `json:"errorCode"`
		Result    map[string]any `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return env.ErrorCode, env.Result
}

func login(t *testing.T, fake *omadatest.Controller) string {
	t.Helper()
	body := `{"omadacId":"` + omadatest.ControllerID + `","client_id":"` + omadatest.ClientID + `","client_secret":"` + omadatest.Secret + `"}`
	code, result := call(t, fake, http.MethodPost, "/openapi/authorize/token?grant_type=client_credentials", "", body)
	if code != 0 {
		t.Fatalf("login: error code %d", code)
	}
	return result["accessToken"].(string)
}

const api = "/openapi/v1/" + omadatest.ControllerID

// The fake has to behave like the controller for the other tests to mean
// anything, so its own contract is checked here.
func TestFakeBehavesLikeTheController(t *testing.T) {
	fake := omadatest.New(t)

	if code, result := call(t, fake, http.MethodGet, "/api/info", "", ""); code != 0 || result["omadacId"] != omadatest.ControllerID {
		t.Errorf("info: code %d, result %v", code, result)
	}

	bad := `{"omadacId":"` + omadatest.ControllerID + `","client_id":"x","client_secret":"y"}`
	if code, _ := call(t, fake, http.MethodPost, "/openapi/authorize/token?grant_type=client_credentials", "", bad); code != -44106 {
		t.Errorf("bad credentials: code %d, want -44106", code)
	}
	if code, _ := call(t, fake, http.MethodPost, "/openapi/authorize/token?grant_type=password", "", bad); code != -44111 {
		t.Errorf("bad grant type: code %d, want -44111", code)
	}
	if code, _ := call(t, fake, http.MethodGet, api+"/sites?page=1&pageSize=10", "nope", ""); code != -44112 {
		t.Errorf("bad token: code %d, want -44112", code)
	}

	token := login(t, fake)
	if fake.Logins() != 1 {
		t.Errorf("logins = %d", fake.Logins())
	}
	if code, _ := call(t, fake, http.MethodGet, api+"/sites", token, ""); code != -1001 {
		t.Errorf("missing paging parameters: code %d, want -1001", code)
	}

	fake.MaxPageSize = 3
	code, result := call(t, fake, http.MethodGet, api+"/sites/site-1/devices?page=2&pageSize=100", token, "")
	if data := result["data"].([]any); code != 0 || len(data) != 1 || result["totalRows"].(float64) != 4 {
		t.Errorf("second page of 4 devices at page size 3: code %d, result %v", code, result)
	}
	code, result = call(t, fake, http.MethodGet, api+"/sites/site-1/devices?page=9&pageSize=100", token, "")
	if data := result["data"].([]any); code != 0 || len(data) != 0 {
		t.Errorf("page past the end: code %d, result %v", code, result)
	}

	if code, _ := call(t, fake, http.MethodGet, api+"/sites/nope/devices?page=1&pageSize=10", token, ""); code != -33004 {
		t.Errorf("unknown site: code %d, want -33004", code)
	}
	// A gateway rename without ledSetting is refused, as on the controller.
	if code, _ := call(t, fake, http.MethodPatch, api+"/sites/site-1/gateways/AA-00-00-00-00-01/general-config", token, `{"name":"x"}`); code != -1001 {
		t.Errorf("gateway rename without ledSetting: code %d, want -1001", code)
	}
	// A device addressed through the endpoint of another type does not exist.
	if code, _ := call(t, fake, http.MethodPatch, api+"/sites/site-1/aps/AA-00-00-00-00-01/general-config", token, `{"name":"x"}`); code != -39050 {
		t.Errorf("gateway through the AP endpoint: code %d, want -39050", code)
	}
	if code, _ := call(t, fake, http.MethodPost, api+"/sites/site-1/clients/CC-00-00-00-00-01/unblock", token, ""); code != -41004 {
		t.Errorf("unblocking a client that is not blocked: code %d, want -41004", code)
	}

	fake.ExpireTokens()
	if code, _ := call(t, fake, http.MethodGet, api+"/sites?page=1&pageSize=10", token, ""); code != -44112 {
		t.Errorf("expired token: code %d, want -44112", code)
	}
	if !fake.Called("GET /sites/site-1/devices") {
		t.Errorf("calls = %v", fake.Calls())
	}
}

func TestFakeRevokeOnLogin(t *testing.T) {
	fake := omadatest.New(t)
	fake.RevokeOnLogin = true
	first := login(t, fake)
	second := login(t, fake)
	if code, _ := call(t, fake, http.MethodGet, api+"/sites?page=1&pageSize=10", first, ""); code != -44112 {
		t.Errorf("the first token must be revoked by the second login: code %d", code)
	}
	if code, _ := call(t, fake, http.MethodGet, api+"/sites?page=1&pageSize=10", second, ""); code != 0 {
		t.Errorf("the second token must work: code %d", code)
	}
}
