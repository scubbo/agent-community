package communityserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackjackson/agent-community/internal/discussion"
	"github.com/oklog/ulid/v2"
	"golang.org/x/sys/unix"
)

const (
	registrationDir = "servers"
	serveLockFile   = ".agent-community-serve.lock"
	controlBodyMax  = 32 * 1024
)

var (
	ErrAlreadyServing      = errors.New("community is already being served")
	ErrControlUnauthorized = errors.New("control request unauthorized")
)

type RuntimeOptions struct {
	CommunityName     string
	CommunityRoot     string
	StateRoot         string
	ListenAddress     string
	PublicURL         string
	SocketPath        string
	AllowLoopbackHTTP bool
}

type Registration struct {
	CommunityName string    `json:"community_name"`
	CommunityRoot string    `json:"community_root"`
	LocalURL      string    `json:"local_url"`
	PublicURL     string    `json:"public_url"`
	ControlSocket string    `json:"control_socket"`
	ControlSecret string    `json:"control_secret"`
	InstanceID    string    `json:"instance_id"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"started_at"`
}

type CreateResult struct {
	Discussion   *discussion.Discussion `json:"discussion"`
	Capabilities map[string]string      `json:"capabilities"`
}

type Runtime struct {
	registration     Registration
	registrationPath string
	lockFile         *os.File
	tcpListener      net.Listener
	controlListener  net.Listener
	publicServer     *http.Server
	controlServer    *http.Server
	serveErrors      chan error

	closeOnce sync.Once
	closeErr  error
}

func Start(opts RuntimeOptions) (*Runtime, error) {
	if err := validateRuntimeOptions(&opts); err != nil {
		return nil, err
	}
	publicURL, err := validatePublicURL(opts.PublicURL, opts.AllowLoopbackHTTP)
	if err != nil {
		return nil, err
	}
	communityRoot, err := filepath.Abs(opts.CommunityRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve community root: %w", err)
	}
	stateRoot, err := filepath.Abs(opts.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve state root: %w", err)
	}
	socketPath, err := filepath.Abs(opts.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("resolve control socket: %w", err)
	}
	// Darwin's sockaddr_un.sun_path is 104 bytes including the terminator;
	// Linux allows 108. Keep the portable limit and fail before bind's opaque
	// "invalid argument" error.
	if len(socketPath) >= 104 {
		return nil, fmt.Errorf("control socket path is too long (%d bytes; maximum 103): %s", len(socketPath), socketPath)
	}

	lockFile, err := acquireServeLock(communityRoot)
	if err != nil {
		return nil, err
	}
	cleanupLock := true
	defer func() {
		if cleanupLock {
			releaseServeLock(lockFile)
		}
	}()

	store, err := discussion.NewStore(communityRoot, discussion.StoreOptions{})
	if err != nil {
		return nil, err
	}
	instanceID, err := newRuntimeID()
	if err != nil {
		return nil, err
	}
	controlSecret, err := randomSecret()
	if err != nil {
		return nil, err
	}
	publicHandler, err := New(store, Options{InstanceID: instanceID})
	if err != nil {
		return nil, err
	}

	tcpListener, err := net.Listen("tcp", opts.ListenAddress)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", opts.ListenAddress, err)
	}
	cleanupTCP := true
	defer func() {
		if cleanupTCP {
			_ = tcpListener.Close()
		}
	}()

	if err := prepareControlSocket(socketPath); err != nil {
		return nil, err
	}
	controlListener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on control socket %s: %w", socketPath, err)
	}
	cleanupControl := true
	defer func() {
		if cleanupControl {
			_ = controlListener.Close()
			_ = os.Remove(socketPath)
		}
	}()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return nil, fmt.Errorf("secure control socket %s: %w", socketPath, err)
	}

	controlMux := http.NewServeMux()
	controlMux.Handle("/healthz", publicHandler)
	controlMux.HandleFunc("/control/v1/discussions", controlCreateHandler(store, controlSecret))
	publicServer := configuredHTTPServer(publicHandler)
	controlServer := configuredHTTPServer(controlMux)
	localURL := "http://" + tcpListener.Addr().String()
	registration := Registration{
		CommunityName: opts.CommunityName,
		CommunityRoot: communityRoot,
		LocalURL:      localURL,
		PublicURL:     publicURL,
		ControlSocket: socketPath,
		ControlSecret: controlSecret,
		InstanceID:    instanceID,
		PID:           os.Getpid(),
		StartedAt:     time.Now().UTC(),
	}
	registrationPath := RegistrationPath(stateRoot, opts.CommunityName)
	if err := writeRegistration(registrationPath, registration); err != nil {
		return nil, err
	}
	cleanupRegistration := true
	defer func() {
		if cleanupRegistration {
			_ = os.Remove(registrationPath)
		}
	}()

	runtime := &Runtime{
		registration:     registration,
		registrationPath: registrationPath,
		lockFile:         lockFile,
		tcpListener:      tcpListener,
		controlListener:  controlListener,
		publicServer:     publicServer,
		controlServer:    controlServer,
		serveErrors:      make(chan error, 2),
	}
	go runtime.serve(publicServer, tcpListener, "public")
	go runtime.serve(controlServer, controlListener, "control")

	cleanupRegistration = false
	cleanupControl = false
	cleanupTCP = false
	cleanupLock = false
	return runtime, nil
}

func (r *Runtime) LocalURL() string {
	return r.registration.LocalURL
}

func (r *Runtime) Wait() error {
	var errs []error
	for range 2 {
		if err := <-r.serveErrors; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Runtime) Close(ctx context.Context) error {
	r.closeOnce.Do(func() {
		var errs []error
		if err := r.publicServer.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown public server: %w", err))
		}
		if err := r.controlServer.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown control server: %w", err))
		}
		if err := os.Remove(r.registrationPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove registration: %w", err))
		}
		if err := os.Remove(r.registration.ControlSocket); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove control socket: %w", err))
		}
		if err := releaseServeLock(r.lockFile); err != nil {
			errs = append(errs, err)
		}
		r.closeErr = errors.Join(errs...)
	})
	return r.closeErr
}

func RegistrationPath(stateRoot, communityName string) string {
	return filepath.Join(stateRoot, registrationDir, communityName+".json")
}

func LoadRegistration(stateRoot, communityName string) (*Registration, error) {
	if !validCommunityName(communityName) {
		return nil, errors.New("invalid community name")
	}
	path := RegistrationPath(stateRoot, communityName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var registration Registration
	if err := decodeStrict(data, &registration); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if registration.CommunityName != communityName || registration.InstanceID == "" || registration.ControlSecret == "" || registration.ControlSocket == "" {
		return nil, fmt.Errorf("validate %s: incomplete or mismatched registration", path)
	}
	return &registration, nil
}

func CreateThroughControl(ctx context.Context, registration *Registration, input discussion.CreateInput) (*CreateResult, error) {
	if registration == nil || registration.ControlSocket == "" || registration.ControlSecret == "" || registration.InstanceID == "" {
		return nil, errors.New("invalid server registration")
	}
	client := unixHTTPClient(registration.ControlSocket)
	healthRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/healthz", nil)
	if err != nil {
		return nil, err
	}
	healthResponse, err := client.Do(healthRequest)
	if err != nil {
		return nil, fmt.Errorf("check control server health: %w", err)
	}
	var health struct {
		OK         bool   `json:"ok"`
		InstanceID string `json:"instance_id"`
	}
	if err := decodeHTTPResponse(healthResponse, &health); err != nil {
		return nil, fmt.Errorf("check control server health: %w", err)
	}
	if !health.OK || health.InstanceID != registration.InstanceID {
		return nil, errors.New("control server instance does not match registration")
	}

	payload := struct {
		TTLSeconds   int64                         `json:"ttl_seconds"`
		Participants []discussion.ParticipantInput `json:"participants"`
	}{
		TTLSeconds:   int64(input.TTL / time.Second),
		Participants: input.Participants,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/control/v1/discussions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+registration.ControlSecret)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("create discussion through control server: %w", err)
	}
	if response.StatusCode == http.StatusUnauthorized {
		response.Body.Close()
		return nil, ErrControlUnauthorized
	}
	var result CreateResult
	if err := decodeHTTPResponse(response, &result); err != nil {
		return nil, fmt.Errorf("create discussion through control server: %w", err)
	}
	return &result, nil
}

func controlCreateHandler(store *discussion.Store, controlSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			writeControlJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method_not_allowed"})
			return
		}
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || !hmac.Equal([]byte(token), []byte(controlSecret)) {
			writeControlJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
			return
		}
		body := http.MaxBytesReader(w, r.Body, controlBodyMax)
		defer body.Close()
		data, err := io.ReadAll(body)
		if err != nil {
			writeControlJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_body"})
			return
		}
		var input struct {
			TTLSeconds   int64                         `json:"ttl_seconds"`
			Participants []discussion.ParticipantInput `json:"participants"`
		}
		if err := decodeStrict(data, &input); err != nil {
			writeControlJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_body"})
			return
		}
		created, capabilities, err := store.Create(discussion.CreateInput{
			TTL:          time.Duration(input.TTLSeconds) * time.Second,
			Participants: input.Participants,
		})
		if err != nil {
			writeControlJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_input"})
			return
		}
		writeControlJSON(w, http.StatusCreated, CreateResult{Discussion: created, Capabilities: capabilities})
	}
}

func configuredHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func unixHTTPClient(socketPath string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func acquireServeLock(communityRoot string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(communityRoot, serveLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open serve lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrAlreadyServing
		}
		return nil, fmt.Errorf("acquire serve lock: %w", err)
	}
	return file, nil
}

func releaseServeLock(file *os.File) error {
	if file == nil {
		return nil
	}
	var errs []error
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		errs = append(errs, fmt.Errorf("release serve lock: %w", err))
	}
	if err := file.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close serve lock: %w", err))
	}
	return errors.Join(errs...)
}

func prepareControlSocket(socketPath string) error {
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create control socket directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure control socket directory: %w", err)
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale control socket: %w", err)
	}
	return nil
}

func writeRegistration(path string, registration Registration) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create registration directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secure registration directory: %w", err)
	}
	data, err := json.MarshalIndent(registration, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".server-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func validateRuntimeOptions(opts *RuntimeOptions) error {
	if !validCommunityName(opts.CommunityName) {
		return errors.New("invalid community name")
	}
	if opts.CommunityRoot == "" || opts.StateRoot == "" || opts.PublicURL == "" || opts.SocketPath == "" {
		return errors.New("community root, state root, public URL, and socket path are required")
	}
	if opts.ListenAddress == "" {
		opts.ListenAddress = "127.0.0.1:7337"
	}
	return nil
}

func validatePublicURL(raw string, allowLoopbackHTTP bool) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", errors.New("public URL must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("public URL must not contain credentials, path, query, or fragment")
	}
	if parsed.Scheme != "https" {
		if parsed.Scheme != "http" || !allowLoopbackHTTP || !isLoopbackHost(parsed.Hostname()) {
			return "", errors.New("public URL must use HTTPS")
		}
	}
	parsed.Path = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validCommunityName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func newRuntimeID() (string, error) {
	id, err := ulid.New(ulid.Timestamp(time.Now().UTC()), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate server instance id: %w", err)
	}
	return id.String(), nil
}

func randomSecret() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate control secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(secret), nil
}

func (r *Runtime) serve(server *http.Server, listener net.Listener, name string) {
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	} else if err != nil {
		err = fmt.Errorf("%s server failed: %w", name, err)
	}
	r.serveErrors <- err
}

func writeControlJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeHTTPResponse(response *http.Response, destination any) error {
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, controlBodyMax+1))
	if err != nil {
		return err
	}
	if len(data) > controlBodyMax {
		return errors.New("response body too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	return decodeStrict(data, destination)
}
