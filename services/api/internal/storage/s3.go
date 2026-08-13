package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	awsAlgorithm     = "AWS4-HMAC-SHA256"
	awsRequestScope  = "aws4_request"
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	unsignedPayload  = "UNSIGNED-PAYLOAD"
)

type S3Config struct {
	Endpoint       string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	SessionToken   string
	ForcePathStyle bool
	HTTPClient     *http.Client
	Now            func() time.Time
}

type S3 struct {
	endpoint       *url.URL
	region         string
	bucket         string
	accessKey      string
	secretKey      string
	sessionToken   string
	forcePathStyle bool
	httpClient     *http.Client
	now            func() time.Time
}

func NewS3(cfg S3Config) (*S3, error) {
	endpoint, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("storage S3 endpoint must be an HTTP(S) origin")
	}
	if endpoint.Path != "" && endpoint.Path != "/" {
		return nil, errors.New("storage S3 endpoint cannot contain a path")
	}
	if strings.TrimSpace(cfg.Region) == "" || strings.TrimSpace(cfg.Bucket) == "" || strings.TrimSpace(cfg.AccessKey) == "" || cfg.SecretKey == "" {
		return nil, errors.New("storage S3 region, bucket and credentials are required")
	}
	if !validBucket(cfg.Bucket) {
		return nil, errors.New("storage S3 bucket name is invalid")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	endpoint.Path = ""
	return &S3{
		endpoint: endpoint, region: strings.TrimSpace(cfg.Region), bucket: strings.TrimSpace(cfg.Bucket),
		accessKey: strings.TrimSpace(cfg.AccessKey), secretKey: cfg.SecretKey, sessionToken: strings.TrimSpace(cfg.SessionToken),
		forcePathStyle: cfg.ForcePathStyle, httpClient: client, now: now,
	}, nil
}

func (s *S3) PresignPut(_ context.Context, objectKey, mediaType, checksumSHA256 string, expires time.Duration) (UploadIntent, error) {
	if expires < time.Minute || expires > 15*time.Minute {
		return UploadIntent{}, errors.New("evidence upload expiry must be between one and fifteen minutes")
	}
	if !validObjectKey(objectKey) || strings.TrimSpace(mediaType) == "" {
		return UploadIntent{}, errors.New("invalid evidence object request")
	}
	checksumBytes, err := base64.StdEncoding.DecodeString(checksumSHA256)
	if err != nil || len(checksumBytes) != sha256.Size {
		return UploadIntent{}, errors.New("checksum must be a base64 SHA-256 digest")
	}

	now := s.now().UTC()
	requestURL := s.objectURL(objectKey)
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := strings.Join([]string{date, s.region, "s3", awsRequestScope}, "/")
	signedHeaders := "content-type;host;x-amz-checksum-sha256"
	query := map[string][]string{
		"X-Amz-Algorithm":     {awsAlgorithm},
		"X-Amz-Credential":    {s.accessKey + "/" + scope},
		"X-Amz-Date":          {amzDate},
		"X-Amz-Expires":       {strconv.FormatInt(int64(expires/time.Second), 10)},
		"X-Amz-SignedHeaders": {signedHeaders},
	}
	if s.sessionToken != "" {
		query["X-Amz-Security-Token"] = []string{s.sessionToken}
	}
	canonicalQuery := canonicalQuery(query)
	canonicalHeaders := "content-type:" + normalizeHeader(mediaType) + "\n" +
		"host:" + normalizeHeader(requestURL.Host) + "\n" +
		"x-amz-checksum-sha256:" + normalizeHeader(checksumSHA256) + "\n"
	canonicalRequest := strings.Join([]string{
		http.MethodPut, escapedPath(requestURL), canonicalQuery, canonicalHeaders, signedHeaders, unsignedPayload,
	}, "\n")
	signature := s.signature(date, amzDate, scope, canonicalRequest)
	requestURL.RawQuery = canonicalQuery + "&X-Amz-Signature=" + signature

	return UploadIntent{
		URL: requestURL.String(),
		RequiredHeaders: map[string]string{
			"Content-Type":          mediaType,
			"x-amz-checksum-sha256": checksumSHA256,
		},
		ExpiresAt: now.Add(expires),
	}, nil
}

func (s *S3) PresignGet(_ context.Context, objectKey string, expires time.Duration) (DownloadIntent, error) {
	if expires < time.Minute || expires > 15*time.Minute {
		return DownloadIntent{}, errors.New("evidence download expiry must be between one and fifteen minutes")
	}
	if !validObjectKey(objectKey) {
		return DownloadIntent{}, errors.New("invalid evidence object request")
	}

	now := s.now().UTC()
	requestURL := s.objectURL(objectKey)
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := strings.Join([]string{date, s.region, "s3", awsRequestScope}, "/")
	query := map[string][]string{
		"X-Amz-Algorithm":     {awsAlgorithm},
		"X-Amz-Credential":    {s.accessKey + "/" + scope},
		"X-Amz-Date":          {amzDate},
		"X-Amz-Expires":       {strconv.FormatInt(int64(expires/time.Second), 10)},
		"X-Amz-SignedHeaders": {"host"},
	}
	if s.sessionToken != "" {
		query["X-Amz-Security-Token"] = []string{s.sessionToken}
	}
	canonical := canonicalQuery(query)
	canonicalHeaders := "host:" + normalizeHeader(requestURL.Host) + "\n"
	canonicalRequest := strings.Join([]string{
		http.MethodGet, escapedPath(requestURL), canonical, canonicalHeaders, "host", unsignedPayload,
	}, "\n")
	requestURL.RawQuery = canonical + "&X-Amz-Signature=" + s.signature(date, amzDate, scope, canonicalRequest)
	return DownloadIntent{URL: requestURL.String(), ExpiresAt: now.Add(expires)}, nil
}

func (s *S3) Stat(ctx context.Context, objectKey string) (ObjectInfo, error) {
	if !validObjectKey(objectKey) {
		return ObjectInfo{}, errors.New("invalid evidence object key")
	}
	now := s.now().UTC()
	requestURL := s.objectURL(objectKey)
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := strings.Join([]string{date, s.region, "s3", awsRequestScope}, "/")
	headers := map[string]string{
		"host":                 requestURL.Host,
		"x-amz-checksum-mode":  "ENABLED",
		"x-amz-content-sha256": emptyPayloadHash,
		"x-amz-date":           amzDate,
	}
	if s.sessionToken != "" {
		headers["x-amz-security-token"] = s.sessionToken
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var canonicalHeaders strings.Builder
	for _, key := range keys {
		canonicalHeaders.WriteString(key)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(normalizeHeader(headers[key]))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(keys, ";")
	canonicalRequest := strings.Join([]string{
		http.MethodHead, escapedPath(requestURL), "", canonicalHeaders.String(), signedHeaders, emptyPayloadHash,
	}, "\n")
	signature := s.signature(date, amzDate, scope, canonicalRequest)
	authorization := awsAlgorithm + " Credential=" + s.accessKey + "/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature

	request, err := http.NewRequestWithContext(ctx, http.MethodHead, requestURL.String(), nil)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("create storage HEAD request: %w", err)
	}
	request.Header.Set("x-amz-checksum-mode", "ENABLED")
	request.Header.Set("x-amz-content-sha256", emptyPayloadHash)
	request.Header.Set("x-amz-date", amzDate)
	if s.sessionToken != "" {
		request.Header.Set("x-amz-security-token", s.sessionToken)
	}
	request.Header.Set("Authorization", authorization)
	response, err := s.httpClient.Do(request)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat storage object: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ObjectInfo{}, ErrNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return ObjectInfo{}, fmt.Errorf("storage HEAD returned %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	checksum := strings.TrimSpace(response.Header.Get("x-amz-checksum-sha256"))
	if checksum == "" {
		return ObjectInfo{}, ErrChecksumAbsent
	}
	size := response.ContentLength
	if size < 0 {
		size, err = strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
		if err != nil {
			return ObjectInfo{}, errors.New("storage object length is unavailable")
		}
	}
	return ObjectInfo{Size: size, ChecksumSHA256: checksum, ETag: strings.Trim(response.Header.Get("ETag"), `"`)}, nil
}

func EqualChecksum(left, right string) bool {
	leftBytes, leftErr := base64.StdEncoding.DecodeString(left)
	rightBytes, rightErr := base64.StdEncoding.DecodeString(right)
	return leftErr == nil && rightErr == nil && len(leftBytes) == sha256.Size && len(rightBytes) == sha256.Size && subtle.ConstantTimeCompare(leftBytes, rightBytes) == 1
}

func HexToBase64SHA256(value string) (string, error) {
	digest, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(digest) != sha256.Size {
		return "", errors.New("sha256 must contain 64 hexadecimal characters")
	}
	return base64.StdEncoding.EncodeToString(digest), nil
}

func (s *S3) objectURL(objectKey string) *url.URL {
	result := *s.endpoint
	if s.forcePathStyle {
		result.Path = "/" + s.bucket + "/" + objectKey
	} else {
		result.Host = s.bucket + "." + result.Host
		result.Path = "/" + objectKey
	}
	return &result
}

func (s *S3) signature(date, amzDate, scope, canonicalRequest string) string {
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{awsAlgorithm, amzDate, scope, hex.EncodeToString(requestHash[:])}, "\n")
	dateKey := hmacSHA256([]byte("AWS4"+s.secretKey), date)
	regionKey := hmacSHA256(dateKey, s.region)
	serviceKey := hmacSHA256(regionKey, "s3")
	signingKey := hmacSHA256(serviceKey, awsRequestScope)
	return hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func escapedPath(value *url.URL) string {
	result := value.EscapedPath()
	if result == "" {
		return "/"
	}
	return result
}

func canonicalQuery(values map[string][]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(values))
	for _, key := range keys {
		items := append([]string(nil), values[key]...)
		sort.Strings(items)
		for _, item := range items {
			parts = append(parts, awsEscape(key)+"="+awsEscape(item))
		}
	}
	return strings.Join(parts, "&")
}

func awsEscape(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func normalizeHeader(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func validObjectKey(value string) bool {
	return value != "" && len(value) <= 512 && !strings.HasPrefix(value, "/") && path.Clean(value) == value && !strings.Contains(value, "\\")
}

func validBucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if !(character == '.' || character == '-' || character >= '0' && character <= '9' || character >= 'a' && character <= 'z') {
			return false
		}
	}
	return true
}
