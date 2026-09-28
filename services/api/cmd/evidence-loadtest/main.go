// Deliberately restricted to an isolated localhost gamics_loadtest database.
// This creates real sessions and uses the unmodified authenticated player routes.
// It does not send 1,000 emails or claim to test the OTP delivery provider.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type player struct {
	user, token string
	intent      intent
}
type intent struct {
	ID      string            `json:"id"`
	URL     string            `json:"uploadUrl"`
	Headers map[string]string `json:"requiredHeaders"`
}
type result struct {
	PlayerID   string  `json:"player_id"`
	EvidenceID string  `json:"evidence_id"`
	Stage      string  `json:"last_stage"`
	Error      string  `json:"error,omitempty"`
	IntentMS   float64 `json:"intent_ms"`
	UploadMS   float64 `json:"upload_ms"`
	CompleteMS float64 `json:"complete_ms"`
	ReadyMS    float64 `json:"ready_after_complete_ms"`
	EndToEndMS float64 `json:"end_to_end_ms"`
	DispatchMS float64 `json:"upload_dispatch_ms"`
	ConnectMS  float64 `json:"upload_connection_ms"`
}
type sample struct {
	At                  string         `json:"at"`
	States              map[string]int `json:"states"`
	Retries             int            `json:"retries"`
	OldestQueuedSeconds float64        `json:"oldest_queued_seconds"`
}
type report struct {
	RunID                string             `json:"run_id"`
	Started              string             `json:"started_utc"`
	Finished             string             `json:"finished_utc"`
	Scope                string             `json:"scope"`
	Players              int                `json:"players"`
	BytesPerImage        int                `json:"bytes_per_image"`
	RequestedBytes       int64              `json:"requested_payload_bytes"`
	AttemptedBytes       int64              `json:"attempted_bytes"`
	UploadedBytes        int64              `json:"successful_upload_bytes"`
	UploadPeak           int64              `json:"peak_upload_requests_in_flight"`
	ConnectedPeak        int64              `json:"peak_upload_requests_with_connection"`
	DurationSeconds      float64            `json:"duration_seconds"`
	UploadWindowSeconds  float64            `json:"upload_window_seconds"`
	UploadThroughputMbps float64            `json:"successful_upload_mbps"`
	Ready                int                `json:"ready"`
	Created              int                `json:"intents_created"`
	Uploaded             int                `json:"uploads_succeeded"`
	Queued               int                `json:"completions_accepted"`
	Counts               map[string]int     `json:"http_statuses"`
	Failures             map[string]int     `json:"failures"`
	Latencies            map[string]latency `json:"latencies"`
	Samples              []sample           `json:"queue_samples"`
	Results              []result           `json:"players_results"`
}
type metrics struct {
	mu                                                              sync.Mutex
	statuses                                                        map[string]int
	uploadActive, uploadPeak, connected, connectedPeak, done, ready atomic.Int64
}

func peak(counter, maximum *atomic.Int64) {
	n := counter.Add(1)
	for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
	}
}
func (m *metrics) status(stage string, status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[stage+"/"+strconv.Itoa(status)]++
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	configPath := flag.String("config", "../../.env.loadtest", "private load-test env file")
	r2Path := flag.String("r2-config", "../../.env.r2", "R2 file (only account/bucket used)")
	count := flag.Int("players", 1, "distinct players, maximum 1000")
	api := flag.String("api", "http://127.0.0.1:8080", "localhost gateway")
	output := flag.String("out", "../../.loadtest/results.json", "redacted JSON report")
	timeout := flag.Duration("timeout", 10*time.Minute, "maximum whole test duration")
	dockerNetwork := flag.Bool("docker-network", false, "use only the isolated gateway/postgres Docker service names")
	probe := flag.Bool("gateway-probe", false, "read-only 1000-request gateway diagnostic; no accounts or R2 writes")
	flag.Parse()
	if *count < 1 || *count > 1000 || *timeout > 20*time.Minute || *timeout < time.Minute {
		return errors.New("players must be 1..1000 and timeout 1..20 minutes")
	}
	origin, err := url.Parse(*api)
	if err != nil || !allowedAPI(origin, *dockerNetwork) {
		return errors.New("test API must be localhost, or exactly http://gateway:8080 in explicit Docker mode")
	}
	if *probe {
		counts := probeGateway(*api)
		fmt.Printf("1000 concurrent read-only gateway probes: %v\n", counts)
		if counts["http_200"] != 1000 {
			return errors.New("local gateway did not serve every first-attempt request")
		}
		return nil
	}
	env, err := readEnv(*configPath)
	if err != nil {
		return err
	}
	r2, err := readEnv(*r2Path)
	if err != nil {
		return err
	}
	if env["POSTGRES_DB"] != "gamics_loadtest" || len(env["AUTH_TOKEN_SECRET"]) < 32 {
		return errors.New("refusing to seed anything except the isolated gamics_loadtest database")
	}
	if !strings.Contains(r2["R2_BUCKET"], "stagin") {
		return errors.New("refusing uploads outside the configured staging bucket")
	}
	expectedHost := r2["R2_ACCOUNT_ID"] + ".r2.cloudflarestorage.com"
	if r2["R2_JURISDICTION"] != "" {
		return errors.New("this local test expects default R2 jurisdiction")
	}
	databaseHost := "127.0.0.1:15432"
	if *dockerNetwork {
		databaseHost = "postgres:5432"
	}
	cfg, err := pgxpool.ParseConfig("postgres://" + databaseHost + "/gamics_loadtest?sslmode=disable")
	if err != nil {
		return errors.New("database configuration failed")
	}
	cfg.ConnConfig.User = env["POSTGRES_USER"]
	cfg.ConnConfig.Password = env["POSTGRES_PASSWORD"]
	cfg.MaxConns = 2
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("cannot configure isolated database")
	}
	defer db.Close()
	if err = db.Ping(ctx); err != nil {
		return errors.New("isolated PostgreSQL is not reachable")
	}
	apiClient := client(*count, 30*time.Second)
	defer apiClient.CloseIdleConnections()
	status, _, err := request(ctx, apiClient, "GET", *api+"/readyz", "", nil, nil)
	if err != nil || status != 200 {
		return errors.New("API readiness check failed")
	}
	body, err := fixture()
	if err != nil {
		return fmt.Errorf("fixture: %w", err)
	}
	digest := sha256.Sum256(body)
	runID := time.Now().UTC().Format("20060102T150405Z") + "-" + auth.RandomID()[:8]
	players, err := seed(ctx, db, env["AUTH_TOKEN_SECRET"], runID, *count)
	if err != nil {
		return err
	}
	rep := report{RunID: runID, Started: time.Now().UTC().Format(time.RFC3339), Scope: "isolated full upload pipeline; seeded authenticated users; synthetic valid PNG; one laptop network; no OTP delivery/HA claim", Players: *count, BytesPerImage: len(body), RequestedBytes: int64(len(body) * *count), Results: make([]result, *count), Failures: map[string]int{}, Latencies: map[string]latency{}}
	m := &metrics{statuses: map[string]int{}}
	for i, p := range players {
		rep.Results[i].PlayerID = p.user
	}
	fmt.Printf("Run %s: %d players, %d bytes each, %d total bytes. Preparing API intents concurrently.\n", runID, *count, len(body), rep.RequestedBytes)
	started := time.Now()
	var group sync.WaitGroup
	start := make(chan struct{})
	payload, _ := json.Marshal(map[string]any{"mediaType": "image/png", "byteSize": len(body), "sha256": hex.EncodeToString(digest[:])})
	for i := range players {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			t := time.Now()
			s, b, e := request(ctx, apiClient, "POST", *api+"/v1/evidence/uploads", players[i].token, payload, nil)
			m.status("intent", s)
			r := &rep.Results[i]
			r.IntentMS = float64(time.Since(t).Microseconds()) / 1000
			r.Stage = "intent"
			if e != nil || s != 201 {
				r.Error = errorCode(s, e)
				return
			}
			var envelope struct {
				Data intent `json:"data"`
			}
			if json.Unmarshal(b, &envelope) != nil || envelope.Data.ID == "" {
				r.Error = "invalid_intent_response"
				return
			}
			u, e := url.Parse(envelope.Data.URL)
			if e != nil || u.Scheme != "https" || u.Host != expectedHost || !strings.HasPrefix(u.Path, "/"+r2["R2_BUCKET"]+"/evidence/") {
				r.Error = "unexpected_upload_destination"
				return
			}
			players[i].intent = envelope.Data
			r.EvidenceID = envelope.Data.ID
			r.Stage = "prepared"
		}(i)
	}
	close(start)
	group.Wait()
	for _, p := range players {
		if p.intent.ID != "" {
			rep.Created++
		}
	}
	fmt.Printf("Intents ready: %d/%d. Releasing uploads together.\n", rep.Created, *count)
	// This is the declared payload for attempted PUTs, not measured wire bytes.
	rep.AttemptedBytes = int64(rep.Created * len(body))
	uploadClient := client(*count, min(*timeout, 5*time.Minute))
	defer uploadClient.CloseIdleConnections()
	stopMonitor := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopMonitor:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				s := queueSample(ctx, db, runID)
				rep.Samples = append(rep.Samples, s)
				fmt.Printf("Progress: finished=%d/%d ready=%d upload_inflight=%d queue=%v retries=%d\n", m.done.Load(), rep.Created, m.ready.Load(), m.uploadActive.Load(), s.States, s.Retries)
			}
		}
	}()
	var uploadFinishedMax atomic.Int64
	start = make(chan struct{})
	burstStart := time.Now()
	for i := range players {
		if players[i].intent.ID == "" {
			continue
		}
		group.Add(1)
		go func(i int) {
			defer group.Done()
			defer m.done.Add(1)
			<-start
			p := players[i]
			r := &rep.Results[i]
			r.Stage = "upload"
			t := time.Now()
			r.DispatchMS = float64(t.Sub(burstStart).Microseconds()) / 1000
			var hasConn atomic.Bool
			trace := &httptrace.ClientTrace{GotConn: func(_ httptrace.GotConnInfo) {
				if hasConn.CompareAndSwap(false, true) {
					r.ConnectMS = float64(time.Since(t).Microseconds()) / 1000
					peak(&m.connected, &m.connectedPeak)
				}
			}}
			peak(&m.uploadActive, &m.uploadPeak)
			s, _, e := request(httptrace.WithClientTrace(ctx, trace), uploadClient, "PUT", p.intent.URL, "", body, p.intent.Headers)
			if hasConn.Load() {
				m.connected.Add(-1)
			}
			m.uploadActive.Add(-1)
			m.status("upload", s)
			r.UploadMS = float64(time.Since(t).Microseconds()) / 1000
			ended := time.Since(burstStart).Nanoseconds()
			for old := uploadFinishedMax.Load(); ended > old && !uploadFinishedMax.CompareAndSwap(old, ended); old = uploadFinishedMax.Load() {
			}
			if e != nil || s < 200 || s >= 300 {
				r.Error = errorCode(s, e)
				return
			}
			r.Stage = "complete"
			t = time.Now()
			s, _, e = request(ctx, apiClient, "POST", *api+"/v1/evidence/uploads/"+p.intent.ID+"/complete", p.token, nil, nil)
			m.status("complete", s)
			r.CompleteMS = float64(time.Since(t).Microseconds()) / 1000
			if e != nil || s != 200 {
				r.Error = errorCode(s, e)
				return
			}
			r.Stage = "poll"
			queued := time.Now()
			delay := 2 * time.Second
			for ctx.Err() == nil {
				timer := time.NewTimer(delay + time.Duration((i*73)%900)*time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					r.Error = "readiness_deadline"
					return
				case <-timer.C:
				}
				s, b, e := request(ctx, apiClient, "GET", *api+"/v1/evidence/uploads/"+p.intent.ID, p.token, nil, nil)
				m.status("poll", s)
				if s == 429 || s == 503 {
					delay = min(10*time.Second, delay+time.Second)
					continue
				}
				if e != nil || s != 200 {
					r.Error = errorCode(s, e)
					return
				}
				var envelope struct {
					Data struct {
						Ready  bool    `json:"ready"`
						Status string  `json:"status"`
						Error  *string `json:"processingErrorCode"`
					} `json:"data"`
				}
				if json.Unmarshal(b, &envelope) != nil {
					r.Error = "invalid_poll_response"
					return
				}
				if envelope.Data.Ready {
					r.Stage = "ready"
					r.ReadyMS = float64(time.Since(queued).Microseconds()) / 1000
					r.EndToEndMS = float64(time.Since(started).Microseconds()) / 1000
					m.ready.Add(1)
					return
				}
				if envelope.Data.Status == "failed" || envelope.Data.Status == "rejected" || envelope.Data.Status == "expired" {
					r.Error = "evidence_" + envelope.Data.Status
					return
				}
				delay = min(10*time.Second, delay+time.Second)
			}
			r.Error = "readiness_deadline"
		}(i)
	}
	close(start)
	group.Wait()
	close(stopMonitor)
	<-monitorDone
	rep.DurationSeconds = time.Since(started).Seconds()
	rep.Finished = time.Now().UTC().Format(time.RFC3339)
	rep.UploadPeak = m.uploadPeak.Load()
	rep.ConnectedPeak = m.connectedPeak.Load()
	rep.Counts = m.statuses
	rep.UploadWindowSeconds = float64(uploadFinishedMax.Load()) / float64(time.Second)
	durations := map[string][]float64{}
	for _, r := range rep.Results {
		durations["intent"] = append(durations["intent"], r.IntentMS)
		if r.UploadMS > 0 {
			durations["upload_all_attempts"] = append(durations["upload_all_attempts"], r.UploadMS)
			durations["dispatch_skew"] = append(durations["dispatch_skew"], r.DispatchMS)
			durations["connection_wait"] = append(durations["connection_wait"], r.ConnectMS)
		}
		if r.Stage == "complete" || r.Stage == "poll" || r.Stage == "ready" {
			rep.Uploaded++
			durations["upload_success"] = append(durations["upload_success"], r.UploadMS)
		}
		if r.Stage == "poll" || r.Stage == "ready" {
			rep.Queued++
			durations["complete"] = append(durations["complete"], r.CompleteMS)
		}
		if r.Stage == "ready" {
			rep.Ready++
			durations["ready_after_complete"] = append(durations["ready_after_complete"], r.ReadyMS)
			durations["end_to_end"] = append(durations["end_to_end"], r.EndToEndMS)
		}
		if r.Error != "" {
			rep.Failures[r.Stage+"/"+r.Error]++
		}
	}
	for name, values := range durations {
		rep.Latencies[name] = summarize(values)
	}
	rep.UploadedBytes = int64(rep.Uploaded * len(body))
	if rep.UploadWindowSeconds > 0 {
		rep.UploadThroughputMbps = float64(rep.UploadedBytes) * 8 / rep.UploadWindowSeconds / 1e6
	}
	rep.Samples = append(rep.Samples, queueSample(context.Background(), db, runID))
	if err = os.MkdirAll(filepath.Dir(*output), 0700); err != nil {
		return errors.New("cannot create report directory")
	}
	encoded, _ := json.MarshalIndent(rep, "", "  ")
	if err = os.WriteFile(*output, encoded, 0600); err != nil {
		return errors.New("cannot save report")
	}
	fmt.Printf("RESULT run=%s created=%d uploaded=%d queued=%d ready=%d/%d duration=%.1fs peak_requests=%d peak_connected=%d throughput=%.2fMbps failures=%v\nReport: %s\n", runID, rep.Created, rep.Uploaded, rep.Queued, rep.Ready, *count, rep.DurationSeconds, rep.UploadPeak, rep.ConnectedPeak, rep.UploadThroughputMbps, rep.Failures, *output)
	if rep.Ready != *count {
		return errors.New("load test had failures; inspect the redacted report")
	}
	return nil
}

func allowedAPI(origin *url.URL, dockerNetwork bool) bool {
	if origin == nil || origin.Scheme != "http" || origin.User != nil || origin.RawQuery != "" || origin.Path != "" || origin.Fragment != "" {
		return false
	}
	if dockerNetwork {
		return origin.Host == "gateway:8080"
	}
	return origin.Hostname() == "127.0.0.1"
}

func probeGateway(origin string) map[string]int {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	c := client(1000, 30*time.Second)
	defer c.CloseIdleConnections()
	var group sync.WaitGroup
	var mu sync.Mutex
	counts := map[string]int{}
	start := make(chan struct{})
	for range 1000 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			s, _, err := request(ctx, c, "GET", origin+"/healthz", "", nil, nil)
			mu.Lock()
			counts[errorCode(s, err)]++
			mu.Unlock()
		}()
	}
	close(start)
	group.Wait()
	return counts
}

func client(concurrency int, timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxConnsPerHost = max(1, concurrency)
	tr.MaxIdleConns = max(64, concurrency)
	tr.MaxIdleConnsPerHost = max(1, concurrency)
	tr.DisableCompression = true
	tr.ForceAttemptHTTP2 = false
	tr.ResponseHeaderTimeout = timeout
	return &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}
func request(ctx context.Context, c *http.Client, method, target, token string, body []byte, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, errors.New("request_invalid")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if !strings.EqualFold(k, "Content-Length") {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, b, err
}
func errorCode(status int, err error) string {
	if err != nil {
		// Never print err.Error(): net/url errors can contain signed R2 URLs.
		var dns *net.DNSError
		if errors.As(err, &dns) {
			return "dns_error"
		}
		var socket syscall.Errno
		if errors.As(err, &socket) {
			return "socket_" + strconv.FormatUint(uint64(socket), 10)
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return "timeout"
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return "connection_eof"
		}
		return "transport_error"
	}
	return "http_" + strconv.Itoa(status)
}
func readEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("private configuration file unavailable")
	}
	defer f.Close()
	m := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			m[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), "\"'")
		}
	}
	return m, s.Err()
}
func seed(ctx context.Context, db *pgxpool.Pool, secret, run string, count int) ([]player, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, errors.New("seed transaction unavailable")
	}
	defer tx.Rollback(ctx)
	batch := &pgx.Batch{}
	players := make([]player, count)
	tm := auth.NewTokenManager(secret, 30*time.Minute)
	for i := range players {
		uid, sid := auth.RandomID(), auth.RandomID()
		_, hash, e := auth.NewRefreshToken()
		if e != nil {
			return nil, errors.New("session generation failed")
		}
		token, _, e := tm.Issue(uid, sid)
		if e != nil {
			return nil, errors.New("token generation failed")
		}
		players[i] = player{user: uid, token: token}
		batch.Queue(`INSERT INTO users (id,email,display_name,country_code,status) VALUES ($1,$2,'Load test player','KE','active')`, uid, fmt.Sprintf("load-%s-%04d@gamics.test", run, i))
		batch.Queue(`INSERT INTO refresh_sessions (id,user_id,token_hash,device_name,user_agent) VALUES ($1,$2,$3,$4,'gamics-loadtest')`, sid, uid, hash, "loadtest:"+run)
	}
	res := tx.SendBatch(ctx, batch)
	for range count * 2 {
		if _, err = res.Exec(); err != nil {
			res.Close()
			return nil, errors.New("test-player seed failed")
		}
	}
	if err = res.Close(); err != nil {
		return nil, errors.New("test-player seed close failed")
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, errors.New("test-player seed commit failed")
	}
	return players, nil
}
func queueSample(ctx context.Context, db *pgxpool.Pool, run string) sample {
	s := sample{At: time.Now().UTC().Format(time.RFC3339), States: map[string]int{}}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := db.Query(ctx, `SELECT e.status,count(*),coalesce(sum(greatest(j.attempts-1,0)),0),coalesce(max(extract(epoch FROM now()-j.created_at)) FILTER (WHERE j.status='queued'),0) FROM evidence_uploads e JOIN users u ON u.id=e.owner_user_id LEFT JOIN evidence_media_processing_jobs j ON j.evidence_id=e.id WHERE u.email LIKE $1 GROUP BY e.status`, "load-"+run+"-%@gamics.test")
	if err != nil {
		s.States["monitor_error"] = 1
		return s
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n, retries int
		var age float64
		if rows.Scan(&state, &n, &retries, &age) != nil {
			s.States["monitor_error"] = 1
			break
		}
		s.States[state] = n
		s.Retries += retries
		s.OldestQueuedSeconds = max(s.OldestQueuedSeconds, age)
	}
	return s
}
