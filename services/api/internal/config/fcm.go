package config

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// FCMServiceAccount is the Firebase service account that push delivery signs
// in with, parsed from FCM_CREDENTIALS_JSON.
type FCMServiceAccount struct {
	ProjectID   string
	ClientEmail string
	TokenURI    string
	PrivateKey  *rsa.PrivateKey
}

var fcmProjectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,61}[a-z0-9]$`)

// errFCMCredentials never quotes the credentials, which hold a private key.
var errFCMCredentials = errors.New("FCM_CREDENTIALS_JSON must be a Firebase service account JSON file, as is or base64-encoded")

// parseFCMServiceAccount reads the service account JSON, given as is or
// base64-encoded so it fits on one line of an env file. Empty means push
// delivery stays off.
func parseFCMServiceAccount(raw, projectID string) (*FCMServiceAccount, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if !strings.HasPrefix(raw, "{") {
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, errFCMCredentials
		}
		raw = string(decoded)
	}
	var file struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if json.Unmarshal([]byte(raw), &file) != nil || file.Type != "service_account" || !strings.Contains(file.ClientEmail, "@") {
		return nil, errFCMCredentials
	}
	block, _ := pem.Decode([]byte(file.PrivateKey))
	if block == nil {
		return nil, errFCMCredentials
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	key, isRSA := parsed.(*rsa.PrivateKey)
	if err != nil || !isRSA {
		return nil, errFCMCredentials
	}
	if file.TokenURI == "" {
		file.TokenURI = "https://oauth2.googleapis.com/token"
	}
	tokenURI, err := url.Parse(file.TokenURI)
	if err != nil || tokenURI.Scheme != "https" || tokenURI.Host == "" {
		return nil, errors.New("the Firebase service account's token_uri must be an HTTPS URL")
	}
	if projectID = strings.TrimSpace(projectID); projectID == "" {
		projectID = file.ProjectID
	}
	if !fcmProjectIDPattern.MatchString(projectID) {
		return nil, errors.New("FCM_PROJECT_ID, or the service account's project_id, must be a Firebase project ID")
	}
	return &FCMServiceAccount{ProjectID: projectID, ClientEmail: file.ClientEmail, TokenURI: file.TokenURI, PrivateKey: key}, nil
}
