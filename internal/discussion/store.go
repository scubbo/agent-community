package discussion

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"
)

type StoreOptions struct {
	Now func() time.Time
}

type Store struct {
	root  string
	now   func() time.Time
	locks sync.Map
}

type discussionRecord struct {
	ID           string              `json:"id"`
	Status       Status              `json:"status"`
	CreatedAt    time.Time           `json:"created_at"`
	ExpiresAt    time.Time           `json:"expires_at"`
	Participants []participantRecord `json:"participants"`
}

type participantRecord struct {
	ID         string           `json:"id"`
	Capability capabilityRecord `json:"capability"`
}

type messageRecord struct {
	Message
	IdempotencyKey string `json:"idempotency_key"`
}

func NewStore(communityRoot string, opts StoreOptions) (*Store, error) {
	root, err := filepath.Abs(communityRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve community root: %w", err)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Store{root: root, now: now}, nil
}

func (s *Store) Create(input CreateInput) (*Discussion, map[string]string, error) {
	if err := validateCreateInput(input); err != nil {
		return nil, nil, err
	}
	now := s.now().UTC()
	id, err := ulid.New(ulid.Timestamp(now), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate discussion id: %w", err)
	}
	record := discussionRecord{
		ID:        id.String(),
		Status:    StatusActive,
		CreatedAt: now,
		ExpiresAt: now.Add(input.TTL),
	}
	capabilities := make(map[string]string, len(input.Participants))
	for _, participant := range input.Participants {
		token, capability, err := newCapability(now, participant.Permissions)
		if err != nil {
			return nil, nil, err
		}
		record.Participants = append(record.Participants, participantRecord{
			ID:         participant.ID,
			Capability: capability,
		})
		capabilities[participant.ID] = token
	}

	dir := s.discussionDir(record.ID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create discussions directory: %w", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create discussion directory: %w", err)
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := writeJSONAtomic(filepath.Join(dir, DiscussionFile), &record); err != nil {
		return nil, nil, fmt.Errorf("write discussion metadata: %w", err)
	}
	if err := writeEmptyFile(filepath.Join(dir, MessagesFile)); err != nil {
		return nil, nil, fmt.Errorf("create discussion messages: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return nil, nil, fmt.Errorf("sync discussion directory: %w", err)
	}
	removeOnError = false
	return publicDiscussion(record, 0), capabilities, nil
}

func (s *Store) Post(discussionID, token string, input PostInput) (*Message, bool, error) {
	if err := validatePostInput(input); err != nil {
		return nil, false, err
	}
	lock := s.discussionLock(discussionID)
	lock.Lock()
	defer lock.Unlock()

	record, participant, err := s.authorize(discussionID, token)
	if err != nil {
		return nil, false, err
	}
	if !hasPermission(participant.Capability, PermissionPost) {
		return nil, false, ErrForbidden
	}
	if err := s.refreshStatus(record); err != nil {
		return nil, false, err
	}
	switch record.Status {
	case StatusEnded:
		return nil, false, ErrDiscussionEnded
	case StatusExpired:
		return nil, false, ErrDiscussionExpired
	}

	messages, err := s.readMessages(*record)
	if err != nil {
		return nil, false, err
	}
	for _, message := range messages {
		if message.Author != participant.ID || message.IdempotencyKey != input.IdempotencyKey {
			continue
		}
		if message.Message.Body != input.Body || message.Message.ReplyTo != input.ReplyTo {
			return nil, false, ErrIdempotencyConflict
		}
		copy := message.Message
		return &copy, true, nil
	}
	if len(messages) >= MaxMessages {
		return nil, false, ErrMessageLimit
	}
	if input.ReplyTo != "" && !containsEarlierMessage(messages, input.ReplyTo) {
		return nil, false, fmt.Errorf("%w: reply_to does not identify an earlier message", ErrInvalidInput)
	}

	now := s.now().UTC()
	id, err := ulid.New(ulid.Timestamp(now), rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("generate message id: %w", err)
	}
	message := Message{
		ID:           id.String(),
		DiscussionID: record.ID,
		Sequence:     len(messages) + 1,
		CreatedAt:    now,
		Author:       participant.ID,
		Body:         input.Body,
		ReplyTo:      input.ReplyTo,
	}
	if err := appendMessage(filepath.Join(s.discussionDir(record.ID), MessagesFile), messageRecord{Message: message, IdempotencyKey: input.IdempotencyKey}); err != nil {
		return nil, false, fmt.Errorf("append discussion message: %w", err)
	}
	return &message, false, nil
}

func (s *Store) Read(discussionID, token string, opts ReadOptions) ([]Message, *Discussion, error) {
	if opts.AfterSequence < 0 || opts.Limit < 0 || opts.Limit > MaxReadLimit {
		return nil, nil, fmt.Errorf("%w: invalid read cursor or limit", ErrInvalidInput)
	}
	lock := s.discussionLock(discussionID)
	lock.Lock()
	defer lock.Unlock()

	record, participant, err := s.authorize(discussionID, token)
	if err != nil {
		return nil, nil, err
	}
	if !hasPermission(participant.Capability, PermissionRead) {
		return nil, nil, ErrForbidden
	}
	if err := s.refreshStatus(record); err != nil {
		return nil, nil, err
	}
	messages, err := s.readMessages(*record)
	if err != nil {
		return nil, nil, err
	}
	filtered := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.Message.Sequence <= opts.AfterSequence {
			continue
		}
		filtered = append(filtered, message.Message)
		if opts.Limit > 0 && len(filtered) == opts.Limit {
			break
		}
	}
	return filtered, publicDiscussion(*record, len(messages)), nil
}

func (s *Store) End(discussionID, token string) (*Discussion, error) {
	lock := s.discussionLock(discussionID)
	lock.Lock()
	defer lock.Unlock()

	record, participant, err := s.authorize(discussionID, token)
	if err != nil {
		return nil, err
	}
	if !hasPermission(participant.Capability, PermissionManage) {
		return nil, ErrForbidden
	}
	if err := s.refreshStatus(record); err != nil {
		return nil, err
	}
	if record.Status == StatusActive {
		record.Status = StatusEnded
		if err := s.writeRecord(*record); err != nil {
			return nil, err
		}
	}
	messages, err := s.readMessages(*record)
	if err != nil {
		return nil, err
	}
	return publicDiscussion(*record, len(messages)), nil
}

func (s *Store) authorize(discussionID, token string) (*discussionRecord, *participantRecord, error) {
	record, err := s.readRecord(discussionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrUnauthorized
		}
		return nil, nil, err
	}
	keyID, ok := capabilityKeyID(token)
	if !ok {
		return nil, nil, ErrUnauthorized
	}
	for i := range record.Participants {
		participant := &record.Participants[i]
		if participant.Capability.KeyID == keyID && capabilityMatches(token, participant.Capability) {
			return record, participant, nil
		}
	}
	return nil, nil, ErrUnauthorized
}

func (s *Store) refreshStatus(record *discussionRecord) error {
	if record.Status != StatusActive || s.now().UTC().Before(record.ExpiresAt) {
		return nil
	}
	record.Status = StatusExpired
	return s.writeRecord(*record)
}

func (s *Store) readRecord(discussionID string) (*discussionRecord, error) {
	if err := validateDiscussionID(discussionID); err != nil {
		return nil, ErrUnauthorized
	}
	path := filepath.Join(s.discussionDir(discussionID), DiscussionFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var record discussionRecord
	if err := decodeStrict(data, &record); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := validateRecord(record, discussionID); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &record, nil
}

func (s *Store) writeRecord(record discussionRecord) error {
	path := filepath.Join(s.discussionDir(record.ID), DiscussionFile)
	if err := writeJSONAtomic(path, &record); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func (s *Store) readMessages(record discussionRecord) ([]messageRecord, error) {
	path := filepath.Join(s.discussionDir(record.ID), MessagesFile)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	participants := make(map[string]struct{}, len(record.Participants))
	for _, participant := range record.Participants {
		participants[participant.ID] = struct{}{}
	}
	messages := make([]messageRecord, 0)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxEncodedLine)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(line) == 0 {
			return nil, fmt.Errorf("decode %s line %d: empty record", path, lineNumber)
		}
		var message messageRecord
		if err := decodeStrict(line, &message); err != nil {
			return nil, fmt.Errorf("decode %s line %d: %w", path, lineNumber, err)
		}
		if err := validateStoredMessage(message, record.ID, lineNumber, participants, messages); err != nil {
			return nil, fmt.Errorf("validate %s line %d: %w", path, lineNumber, err)
		}
		messages = append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return messages, nil
}

func (s *Store) discussionDir(discussionID string) string {
	return filepath.Join(s.root, DiscussionsDir, discussionID)
}

func (s *Store) discussionLock(discussionID string) *sync.Mutex {
	lock, _ := s.locks.LoadOrStore(discussionID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func validateCreateInput(input CreateInput) error {
	if input.TTL <= 0 || input.TTL > MaxTTL {
		return fmt.Errorf("%w: ttl must be greater than zero and no more than %s", ErrInvalidInput, MaxTTL)
	}
	if len(input.Participants) == 0 {
		return fmt.Errorf("%w: at least one participant is required", ErrInvalidInput)
	}
	seen := make(map[string]struct{}, len(input.Participants))
	for _, participant := range input.Participants {
		if !validParticipantID(participant.ID) {
			return fmt.Errorf("%w: invalid participant id %q", ErrInvalidInput, participant.ID)
		}
		if _, exists := seen[participant.ID]; exists {
			return fmt.Errorf("%w: duplicate participant id %q", ErrInvalidInput, participant.ID)
		}
		seen[participant.ID] = struct{}{}
		permissionSeen := make(map[Permission]struct{}, len(participant.Permissions))
		for _, permission := range participant.Permissions {
			if !validPermission(permission) {
				return fmt.Errorf("%w: unknown permission %q", ErrInvalidInput, permission)
			}
			if _, exists := permissionSeen[permission]; exists {
				return fmt.Errorf("%w: duplicate permission %q", ErrInvalidInput, permission)
			}
			permissionSeen[permission] = struct{}{}
		}
	}
	return nil
}

func validatePostInput(input PostInput) error {
	if input.Body == "" {
		return fmt.Errorf("%w: body must not be empty", ErrInvalidInput)
	}
	if len([]byte(input.Body)) > MaxBodyBytes {
		return fmt.Errorf("%w: body exceeds %d bytes", ErrInvalidInput, MaxBodyBytes)
	}
	if len(input.IdempotencyKey) < 1 || len(input.IdempotencyKey) > 128 {
		return fmt.Errorf("%w: idempotency key must be 1-128 characters", ErrInvalidInput)
	}
	for _, character := range input.IdempotencyKey {
		if character < 0x20 || character > 0x7e {
			return fmt.Errorf("%w: idempotency key must be printable ASCII", ErrInvalidInput)
		}
	}
	return nil
}

func validateRecord(record discussionRecord, expectedID string) error {
	if record.ID != expectedID {
		return fmt.Errorf("discussion id %q does not match directory %q", record.ID, expectedID)
	}
	if record.Status != StatusActive && record.Status != StatusEnded && record.Status != StatusExpired {
		return fmt.Errorf("invalid status %q", record.Status)
	}
	if record.CreatedAt.IsZero() || record.ExpiresAt.IsZero() || !record.ExpiresAt.After(record.CreatedAt) {
		return errors.New("invalid discussion timestamps")
	}
	if len(record.Participants) == 0 {
		return errors.New("discussion has no participants")
	}
	seen := make(map[string]struct{}, len(record.Participants))
	seenCapabilityKeys := make(map[string]struct{}, len(record.Participants))
	for _, participant := range record.Participants {
		if !validParticipantID(participant.ID) {
			return fmt.Errorf("invalid participant id %q", participant.ID)
		}
		if _, exists := seen[participant.ID]; exists {
			return fmt.Errorf("duplicate participant id %q", participant.ID)
		}
		seen[participant.ID] = struct{}{}
		if participant.Capability.KeyID == "" || participant.Capability.TokenDigest == "" {
			return fmt.Errorf("participant %q has invalid capability", participant.ID)
		}
		if _, exists := seenCapabilityKeys[participant.Capability.KeyID]; exists {
			return fmt.Errorf("duplicate capability key id %q", participant.Capability.KeyID)
		}
		seenCapabilityKeys[participant.Capability.KeyID] = struct{}{}
		if _, err := hex.DecodeString(participant.Capability.TokenDigest); err != nil || len(participant.Capability.TokenDigest) != sha256.Size*2 {
			return fmt.Errorf("participant %q has invalid capability digest", participant.ID)
		}
		if participant.Capability.CreatedAt.IsZero() {
			return fmt.Errorf("participant %q has no capability creation timestamp", participant.ID)
		}
		permissionSeen := make(map[Permission]struct{}, len(participant.Capability.Permissions))
		for _, permission := range participant.Capability.Permissions {
			if !validPermission(permission) {
				return fmt.Errorf("participant %q has unknown permission %q", participant.ID, permission)
			}
			if _, exists := permissionSeen[permission]; exists {
				return fmt.Errorf("participant %q has duplicate permission %q", participant.ID, permission)
			}
			permissionSeen[permission] = struct{}{}
		}
	}
	return nil
}

func validateStoredMessage(message messageRecord, discussionID string, lineNumber int, participants map[string]struct{}, earlier []messageRecord) error {
	if message.Message.ID == "" || message.Message.DiscussionID != discussionID {
		return errors.New("invalid message identity")
	}
	if message.Message.Sequence != lineNumber {
		return fmt.Errorf("sequence %d is not contiguous; expected %d", message.Message.Sequence, lineNumber)
	}
	if message.Message.CreatedAt.IsZero() {
		return errors.New("message has no creation timestamp")
	}
	if _, exists := participants[message.Message.Author]; !exists {
		return fmt.Errorf("unknown author %q", message.Message.Author)
	}
	if err := validatePostInput(PostInput{IdempotencyKey: message.IdempotencyKey, Body: message.Message.Body, ReplyTo: message.Message.ReplyTo}); err != nil {
		return err
	}
	if message.Message.ReplyTo != "" && !containsEarlierMessage(earlier, message.Message.ReplyTo) {
		return errors.New("reply_to does not identify an earlier message")
	}
	for _, prior := range earlier {
		if prior.Message.ID == message.Message.ID {
			return fmt.Errorf("duplicate message id %q", message.Message.ID)
		}
		if prior.Message.Author == message.Message.Author && prior.IdempotencyKey == message.IdempotencyKey {
			return fmt.Errorf("duplicate idempotency key %q for author %q", message.IdempotencyKey, message.Message.Author)
		}
	}
	return nil
}

func validateDiscussionID(id string) error {
	if _, err := ulid.ParseStrict(id); err != nil {
		return err
	}
	return nil
}

func validParticipantID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, character := range id {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validPermission(permission Permission) bool {
	switch permission {
	case PermissionRead, PermissionPost, PermissionSubscribe, PermissionManage:
		return true
	default:
		return false
	}
}

func containsEarlierMessage(messages []messageRecord, id string) bool {
	for _, message := range messages {
		if message.Message.ID == id {
			return true
		}
	}
	return false
}

func publicDiscussion(record discussionRecord, lastSequence int) *Discussion {
	discussion := &Discussion{
		ID:           record.ID,
		Status:       record.Status,
		CreatedAt:    record.CreatedAt,
		ExpiresAt:    record.ExpiresAt,
		LastSequence: lastSequence,
		Participants: make([]Participant, 0, len(record.Participants)),
	}
	for _, participant := range record.Participants {
		discussion.Participants = append(discussion.Participants, Participant{ID: participant.ID})
	}
	return discussion
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func appendMessage(path string, message messageRecord) error {
	line, err := json.Marshal(message)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	written := 0
	for written < len(line) {
		n, writeErr := f.Write(line[written:])
		written += n
		if writeErr != nil {
			_ = f.Truncate(offset)
			return writeErr
		}
		if n == 0 {
			_ = f.Truncate(offset)
			return io.ErrShortWrite
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Truncate(offset)
		return err
	}
	return nil
}

func writeEmptyFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".discussion-*.tmp")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
