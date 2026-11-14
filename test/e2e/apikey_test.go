package e2e

import (
	"net/http"
	"strings"
)

// postV1 posts a JSON body to the client's OpenAI endpoint with its API key,
// as a tool configured with it does.
func postV1(url, key, body string) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	return http.DefaultClient.Do(req)
}
