package facebook

import (
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
)

// Authenticator provides the authentication functionality for Facebook users
// using Facebook's OAuth
type Authenticator interface {
	// AuthURL returns a Facebook URL the user should be redirect to. The user
	// will then be asked to log in by Facebook at that URL and will be redirected
	// back to our API by Facebook.
	AuthURL(session string) string
	// CreateTransport returns an http.RoundTripper that can be attached to an
	// http.Client which will then have an authenticated connection
	CreateTransport(code string) (http.RoundTripper, error)
}

// NewAuthenticator initializes and returns an Authenticator
func NewAuthenticator(conf Config, domain string) (a Authenticator, err error) {
	opts, err := oauth2.New(
		oauth2.Client(conf.AppID, conf.AppSecret),
		oauth2.RedirectURL(domain+"api/v1/oauth/facebook/redirect"),
		oauth2.Scope("manage_pages", "publish_actions"),
		oauth2.Endpoint(
			"https://www.facebook.com/dialog/oauth",
			"https://graph.facebook.com/oauth/access_token",
		),
	)
	a = authenticator{opts}
	return
}

type authenticator struct {
	*oauth2.Options
}

func (a authenticator) AuthURL(session string) string {
	return a.AuthCodeURL(session, "offline", "auto")
}

func (a authenticator) CreateTransport(code string) (transport http.RoundTripper, err error) {
	if _, err = fmt.Scan(&code); err != nil {
		return
	}
	transport, err = a.NewTransportFromCode(code)
	return
}
