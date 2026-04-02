package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	jmapSessionURL = "https://api.fastmail.com/jmap/session"
	jmapAPIURL     = "https://api.fastmail.com/jmap/api/"

	capabilityCore       = "urn:ietf:params:jmap:core"
	capabilityMaskedMail = "https://www.fastmail.com/dev/maskedemail"
)

type jmapSession struct {
	PrimaryAccounts map[string]string `json:"primaryAccounts"`
}

type jmapRequest struct {
	Using       []string `json:"using"`
	MethodCalls [][]any  `json:"methodCalls"`
}

type maskedEmailCreate struct {
	EmailPrefix string `json:"emailPrefix,omitempty"`
	ForDomain   string `json:"forDomain"`
	Description string `json:"description"`
	State       string `json:"state"`
}

type maskedEmailSetArgs struct {
	AccountID string                       `json:"accountId"`
	Create    map[string]maskedEmailCreate `json:"create"`
}

type maskedEmailSetResponse struct {
	Created map[string]struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"created"`
	NotCreated map[string]struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	} `json:"notCreated"`
}

type jmapResponse struct {
	MethodResponses [][]json.RawMessage `json:"methodResponses"`
}

func fetchAccountID(ctx context.Context, client *http.Client, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jmapSessionURL, nil)
	if err != nil {
		return "", fmt.Errorf("building session request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("session request returned %d: %s", resp.StatusCode, string(body))
	}

	var session jmapSession
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return "", fmt.Errorf("decoding session: %w", err)
	}

	if id, ok := session.PrimaryAccounts[capabilityMaskedMail]; ok {
		return id, nil
	}
	if id, ok := session.PrimaryAccounts[capabilityCore]; ok {
		return id, nil
	}
	return "", fmt.Errorf("no account ID found in session response")
}

func createMaskedEmail(ctx context.Context, client *http.Client, token, accountID, prefix, domain, description string) (string, error) {
	body := jmapRequest{
		Using: []string{capabilityCore, capabilityMaskedMail},
		MethodCalls: [][]any{
			{
				"MaskedEmail/set",
				maskedEmailSetArgs{
					AccountID: accountID,
					Create: map[string]maskedEmailCreate{
						"new-masked-email": {
							EmailPrefix: prefix,
							ForDomain:   domain,
							Description: description,
							State:       "enabled",
						},
					},
				},
				"m1",
			},
		},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, jmapAPIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("JMAP request returned %d: %s", resp.StatusCode, string(respBody))
	}

	var jmapResp jmapResponse
	if err := json.NewDecoder(resp.Body).Decode(&jmapResp); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}

	if len(jmapResp.MethodResponses) == 0 || len(jmapResp.MethodResponses[0]) < 2 {
		return "", fmt.Errorf("unexpected JMAP response structure")
	}

	var setResp maskedEmailSetResponse
	if err := json.Unmarshal(jmapResp.MethodResponses[0][1], &setResp); err != nil {
		return "", fmt.Errorf("decoding set response: %w", err)
	}

	if created, ok := setResp.Created["new-masked-email"]; ok {
		return created.Email, nil
	}

	if notCreated, ok := setResp.NotCreated["new-masked-email"]; ok {
		return "", fmt.Errorf("masked email not created: %s: %s", notCreated.Type, notCreated.Description)
	}

	return "", fmt.Errorf("no created or notCreated entry in response")
}
