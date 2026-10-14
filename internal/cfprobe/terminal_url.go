package cfprobe

import "net/url"

func terminalWebSocketURL(workerURL, id, secret string) (string, error) {
	u, err := url.Parse(workerURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = "/agent/term"
	q := u.Query()
	q.Set("id", id)
	q.Set("secret", secret)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
