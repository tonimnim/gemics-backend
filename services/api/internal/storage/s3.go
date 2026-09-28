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
	PublicEndpoint string
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
	internalEndpoint *url.URL
	publicEndpoint   *url.URL
	region           string
	bucket           string
	accessKey        string
	secretKey        string
	sessionToken     string
	forcePathStyle   bool
	httpClient       *http.Client
	now              func() time.Time
}

func NewS3(cfg S3Config) (*S3, error) {
	internalEndpoint, err := parseS3Origin(cfg.Endpoint, "endpoint")
	if err != nil {
		return nil, err
	}
	publicRaw := strings.TrimSpace(cfg.PublicEndpoint)
	if publicRaw == "" {
		publicRaw = cfg.Endpoint
	}
	publicEndpoint, err := parseS3Origin(publicRaw, "public endpoint")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Region) == "" || strings.TrimSpace(cfg.Bucket) == "" || strings.TrimSpace(cfg.AccessKey) == "" || cfg.SecretKey == "" {
		return nil, errors.New("storage S3 region, bucket and credentials are required")
	}
	if !validBucket(cfg.Bucket) {
		return nil, errors.New("storage S3 bucket name is invalid")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = NewHTTPClient(10 * time.Second)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &S3{
		internalEndpoint: internalEndpoint, publicEndpoint: publicEndpoint,
		region: strings.TrimSpace(cfg.Region), bucket: strings.TrimSpace(cfg.Bucket),
		accessKey: strings.TrimSpace(cfg.AccessKey), secretKey: cfg.SecretKey, sessionToken: strings.TrimSpace(cfg.SessionToken),
		forcePathStyle: cfg.ForcePathStyle, httpClient: client, now: now,
	}, nil
}

func (s *S3) PresignPut(_ context.Context, objectKey, mediaType, checksumSHA256 string, byteSize int64, expires time.Duration) (UploadIntent, error) {
	if expires < time.Minute || expires > 15*time.Minute {
		return UploadIntent{}, errors.New("evidence upload expiry must be between one and fifteen minutes")
	}
	if !validObjectKey(objectKey) || strings.TrimSpace(mediaType) == "" || byteSize < 1 || byteSize > 250<<20 {
		return UploadIntent{}, errors.New("invalid evidence object request")
	}
	checksumBytes, err := base64.StdEncoding.DecodeString(checksumSHA256)
	if err != nil || len(checksumBytes) != sha256.Size {
		return UploadIntent{}, errors.New("checksum must be a base64 SHA-256 digest")
	}

	now := s.now().UTC()
	requestURL := s.publicObjectURL(objectKey)
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := strings.Join([]string{date, s.region, "s3", awsRequestScope}, "/")
	signedHeaders := "content-length;content-type;host;if-none-match;x-amz-checksum-sha256"
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
	canonicalHeaders := "content-length:" + strconv.FormatInt(byteSize, 10) + "\n" +
		"content-type:" + normalizeHeader(mediaType) + "\n" +
		"host:" + normalizeHeader(requestURL.Host) + "\n" +
		"if-none-match:*\n" +
		"x-amz-checksum-sha256:" + normalizeHeader(checksumSHA256) + "\n"
	canonicalRequest := strings.Join([]string{
		http.MethodPut, escapedPath(requestURL), canonicalQuery, canonicalHeaders, signedHeaders, unsignedPayload,
	}, "\n")
	signature := s.signature(date, amzDate, scope, canonicalRequest)
	requestURL.RawQuery = canonicalQuery + "&X-Amz-Signature=" + signature

	return UploadIntent{
		URL: requestURL.String(),
		RequiredHeaders: map[string]string{
			"Content-Length":        strconv.FormatInt(byteSize, 10),
			"Content-Type":          mediaType,
			"If-None-Match":         "*",
			"x-amz-checksum-sha256": checksumSHA256,
		},
		ExpiresAt: now.Add(expires),
	}, nil
}

func (s *S3) PresignGet(_ context.Context, objectKey string, expires time.Duration) (DownloadIntent, error) {
	return s.presignGet(s.publicObjectURL(objectKey), objectKey, expires)
}

func (s *S3) presignGet(requestURL *url.URL, objectKey string, expires time.Duration) (DownloadIntent, error) {
	if expires < time.Minute || expires > 15*time.Minute {
		return DownloadIntent{}, errors.New("evidence download expiry must be between one and fifteen minutes")
	}
	if !validObjectKey(objectKey) {
		return DownloadIntent{}, errors.New("invalid evidence object request")
	}

	now := s.now().UTC()
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
	requestURL := s.internalObjectURL(objectKey)
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
		return ObjectInfo{}, errors.New("create storage HEAD request")
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
		return ObjectInfo{}, newTransportError("head", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ObjectInfo{}, ErrNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ObjectInfo{}, &StatusError{Op: "head", StatusCode: response.StatusCode}
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
	return ObjectInfo{Size: size, MediaType: response.Header.Get("Content-Type"), ChecksumSHA256: checksum, ETag: strings.Trim(response.Header.Get("ETag"), `"`)}, nil
}

// Read uses the private endpoint and bounds the response even if the server lies
// about Content-Length. Failures are typed and never carry the signed URL.
func (s *S3) Read(ctx context.Context, objectKey string, maxBytes int64) ([]byte, error) {
	if maxBytes < 1 || maxBytes > 25<<20 {
		return nil, ErrObjectTooLarge
	}
	intent, err := s.presignGet(s.internalObjectURL(objectKey), objectKey, time.Minute)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, intent.URL, nil)
	if err != nil {
		return nil, errors.New("create private object request")
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, newTransportError("get", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Op: "get", StatusCode: resp.StatusCode}
	}
	if resp.ContentLength > maxBytes {
		return nil, ErrObjectTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, newTransportError("get", err)
	}
	if int64(len(body)) > maxBytes {
		return nil, ErrObjectTooLarge
	}
	return body, nil
}

// Reuse connections, bound provider concurrency and never follow a redirect
// carrying storage credentials to another origin.
func NewHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 128
	transport.MaxIdleConnsPerHost = 64
	transport.MaxConnsPerHost = 64
	transport.ResponseHeaderTimeout = timeout
	transport.DisableCompression = true
	return &http.Client{Timeout: timeout, Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
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

func (s *S3) internalObjectURL(objectKey string) *url.URL {
	return s.objectURL(s.internalEndpoint, objectKey)
}

func (s *S3) publicObjectURL(objectKey string) *url.URL {
	return s.objectURL(s.publicEndpoint, objectKey)
}

func (s *S3) objectURL(endpoint *url.URL, objectKey string) *url.URL {
	result := *endpoint
	if s.forcePathStyle {
		result.Path = "/" + s.bucket + "/" + objectKey
	} else {
		result.Host = s.bucket + "." + result.Host
		result.Path = "/" + objectKey
	}
	return &result
}

func parseS3Origin(raw, label string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.Opaque != "" {
		return nil, fmt.Errorf("storage S3 %s must be an HTTP(S) origin", label)
	}
	if endpoint.Path != "" && endpoint.Path != "/" {
		return nil, fmt.Errorf("storage S3 %s cannot contain a path", label)
	}
	endpoint.Path = ""
	endpoint.RawPath = ""
	return endpoint, nil
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
