package replaycapture

import (
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Store has no plaintext fallback or QA BlobStore/export integration.
type Store struct {
	root      *os.Root
	key       *rsa.PublicKey
	mu        sync.Mutex
	slots     chan struct{}
	queue     chan Request
	done      chan struct{}
	queueMu   sync.RWMutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func Open(dir string, publicKey []byte) (*Store, error) {
	key, err := PublicKey(publicKey)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return nil, errors.New("replay directory must be a private directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root, key: key, slots: make(chan struct{}, 4), queue: make(chan Request, 4), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for req := range s.queue {
			if err := s.Save(req, time.Now()); err != nil {
				log.Print("release replay encrypted capture unavailable")
			}
			clear(req.Body)
		}
	}()
	return s, nil
}
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.queueMu.Lock()
		s.closed = true
		close(s.queue)
		s.queueMu.Unlock()
		<-s.done
		s.closeErr = s.root.Close()
	})
	return s.closeErr
}

// Enqueue moves encryption/fsync off request completion. A full queue is a
// collection gap; it never delays a customer's response or writes plaintext.
func (s *Store) Enqueue(req Request) bool {
	s.queueMu.RLock()
	defer s.queueMu.RUnlock()
	if s.closed {
		return false
	}
	req.Body = append([]byte(nil), req.Body...)
	select {
	case s.queue <- req:
		return true
	default:
		clear(req.Body)
		return false
	}
}
func (s *Store) Acquire() bool {
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		return false
	}
}
func (s *Store) Release() { <-s.slots }

func Combination(req Request) string {
	raw, _ := json.Marshal([]any{req.UserID, req.Model, req.Endpoint, req.Stream, req.Tools, req.Multimodal})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Prune runs independently of traffic. Unknown entries fail closed rather than
// following links or deleting unrelated files in an operator-selected directory.
func (s *Store) Prune(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.fileLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	_, err = s.entries(now)
	return err
}
func (s *Store) entries(now time.Time) ([]os.FileInfo, error) {
	f, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(MaxFiles + 3)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > MaxFiles+2 {
		return nil, errors.New("replay file budget exceeded")
	}
	var out []os.FileInfo
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || (!strings.HasSuffix(entry.Name(), ".json") && entry.Name() != ".pending" && entry.Name() != ".lock") {
			return nil, errors.New("unexpected replay directory entry")
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return nil, errors.New("invalid replay file permissions")
		}
		if entry.Name() == ".lock" {
			continue
		}
		if entry.Name() == ".pending" || !now.Before(info.ModTime().Add(Retention)) {
			if err = s.root.Remove(entry.Name()); err != nil {
				return nil, err
			}
		} else {
			out = append(out, info)
		}
	}
	if len(out) > MaxFiles {
		return nil, errors.New("replay file budget exceeded")
	}
	return out, nil
}

func (s *Store) Save(req Request, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.fileLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	entries, err := s.entries(now)
	if err != nil {
		return err
	}
	prefix := Combination(req) + "-"
	var same []os.FileInfo
	var total int64
	for _, e := range entries {
		total += e.Size()
		if strings.HasPrefix(e.Name(), prefix) {
			same = append(same, e)
		}
	}
	// Retain the first two complete samples; expired samples are replaced. This
	// prevents a busy conversation from continuously evicting its shorter evidence.
	if len(same) >= PerCombination {
		return nil
	}
	raw, err := Encrypt(s.key, req)
	if err != nil {
		return err
	}
	if len(raw) > MaxEnvelopeBytes || total+int64(len(raw)) > MaxStoreBytes || len(entries) >= MaxFiles {
		return errors.New("replay storage budget exhausted")
	}
	hash := sha256.Sum256([]byte(req.RequestID))
	name := prefix + hex.EncodeToString(hash[:]) + ".json"
	if _, err := s.root.Lstat(name); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := s.root.OpenFile(".pending", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, writeErr := f.Write(raw)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = s.root.Remove(".pending")
		return errors.New("replay encrypted write failed")
	}
	return s.root.Rename(".pending", name)
}

// ReadEnvelopes is shared by the offline decrypt command. It never follows links.
func ReadEnvelopes(dir string) ([][]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(MaxFiles + 3)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > MaxFiles+2 {
		return nil, errors.New("replay file budget exceeded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out [][]byte
	var total int64
	for _, entry := range entries {
		if (entry.Name() == ".pending" || entry.Name() == ".lock") && entry.Type().IsRegular() {
			continue // The writer publishes only complete ciphertext via atomic rename.
		}
		if entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			return nil, errors.New("unexpected replay file")
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > MaxEnvelopeBytes || info.Mode().Perm() != 0600 {
			return nil, errors.New("invalid replay file")
		}
		total += info.Size()
		if total > MaxStoreBytes {
			return nil, errors.New("replay byte budget exceeded")
		}
		raw, err := root.ReadFile(entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	if len(out) > MaxFiles {
		return nil, errors.New("replay file budget exceeded")
	}
	return out, nil
}

// PruneDirectory keeps expiry cleanup active after capture is disabled and no
// public key is loaded. Missing directories do not activate or create a store.
func PruneDirectory(dir string, now time.Time) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return errors.New("invalid replay cleanup directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	s := &Store{root: root}
	return s.Prune(now)
}

// Both blue/green colors can share DATA_DIR. The file lock serializes budget,
// expiry and atomic publication across processes as well as local goroutines.
func (s *Store) fileLock() (*os.File, error) {
	f, err := s.root.OpenFile(".lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		_ = f.Close()
		return nil, errors.New("invalid replay lock file")
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil // Closing releases the advisory lock, including on process exit.
}
