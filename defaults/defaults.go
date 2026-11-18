package defaults

const (
	Branch = "main"
	Port   = 80

	// GitHubClientID is the public Poof GitHub App (slug poof-app). The
	// device flow needs only this id, which is not a secret; a self-hoster
	// who prefers their own App overrides it with `poof config set
	// github-client-id`.
	GitHubClientID = "Iv23lixnkW9mw6yZpVa2"
	// GitHubAppURL is where the App is installed on an account.
	GitHubAppURL = "https://github.com/apps/poof-app"
)
