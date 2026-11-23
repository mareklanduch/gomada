package omada

import "time"

// renewMargin is how long before its expiry a token stops being used, so a
// request never leaves with a token that expires in flight.
const renewMargin = time.Minute

// fallbackTokenLifetime is used when the controller does not say how long a
// token lasts.
const fallbackTokenLifetime = 10 * time.Minute

// Token is an Open API access token.
type Token struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Valid reports whether the token can still be used at the given time. A
// token with less than a minute left counts as expired.
func (t Token) Valid(now time.Time) bool {
	return t.AccessToken != "" && now.Add(renewMargin).Before(t.ExpiresAt)
}

// TokenCache keeps a token between runs. Implementations must be safe to use
// from one goroutine at a time; the client serializes its calls.
type TokenCache interface {
	// Load returns the cached token, if there is one.
	Load() (Token, bool)
	// Save stores a freshly issued token.
	Save(Token)
	// Clear forgets the cached token.
	Clear()
}
