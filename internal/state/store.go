package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var ErrCorrupt = errors.New("persistent state is corrupt")

type Store struct {
	dir      string
	path     string
	lockPath string
	mu       sync.Mutex
}

func NewStore(dir string) *Store {
	dir = filepath.Clean(strings.TrimSpace(dir))
	return &Store{
		dir:      dir,
		path:     filepath.Join(dir, "state.json"),
		lockPath: filepath.Join(dir, "state.lock"),
	}
}

func (s *Store) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

func (s *Store) Update(fn func(*State) error) error {
	if fn == nil {
		return errors.New("state update function is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if errDir := ensurePrivateDir(s.dir); errDir != nil {
		return errDir
	}
	lock, errLock := acquireFileLock(s.lockPath)
	if errLock != nil {
		return fmt.Errorf("acquire state lock: %w", errLock)
	}
	defer lock.Close()
	current, errLoad := s.loadUnlocked()
	if errLoad != nil {
		return errLoad
	}
	if errUpdate := fn(&current); errUpdate != nil {
		return errUpdate
	}
	return s.saveUnlocked(current)
}

func (s *Store) loadUnlocked() (State, error) {
	raw, errRead := os.ReadFile(s.path)
	if errors.Is(errRead, os.ErrNotExist) {
		return New(), nil
	}
	if errRead != nil {
		return State{}, fmt.Errorf("read persistent state: %w", errRead)
	}
	if len(raw) > 8<<20 {
		return State{}, ErrCorrupt
	}
	var current State
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&current); errDecode != nil {
		return State{}, fmt.Errorf("%w: invalid JSON", ErrCorrupt)
	}
	if errTrailing := ensureJSONEOF(decoder); errTrailing != nil {
		return State{}, fmt.Errorf("%w: trailing data", ErrCorrupt)
	}
	if current.SchemaVersion != SchemaVersion {
		return State{}, fmt.Errorf("%w: unsupported schema version", ErrCorrupt)
	}
	current.Normalize()
	return current, nil
}

func (s *Store) saveUnlocked(current State) error {
	current.SchemaVersion = SchemaVersion
	current.Normalize()
	raw, errMarshal := json.MarshalIndent(current, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("encode persistent state: %w", errMarshal)
	}
	raw = append(raw, '\n')
	if errDir := ensurePrivateDir(s.dir); errDir != nil {
		return errDir
	}
	temp, errCreate := os.CreateTemp(s.dir, ".state-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create temporary state: %w", errCreate)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if errChmod := temp.Chmod(0o600); errChmod != nil {
		cleanup()
		return fmt.Errorf("set temporary state permissions: %w", errChmod)
	}
	if _, errWrite := temp.Write(raw); errWrite != nil {
		cleanup()
		return fmt.Errorf("write temporary state: %w", errWrite)
	}
	if errSync := temp.Sync(); errSync != nil {
		cleanup()
		return fmt.Errorf("sync temporary state: %w", errSync)
	}
	if errClose := temp.Close(); errClose != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close temporary state: %w", errClose)
	}
	if errRename := replaceFile(tempPath, s.path); errRename != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace persistent state: %w", errRename)
	}
	if errChmod := os.Chmod(s.path, 0o600); errChmod != nil {
		return fmt.Errorf("set persistent state permissions: %w", errChmod)
	}
	return syncDirectory(s.dir)
}

func ensurePrivateDir(dir string) error {
	if strings.TrimSpace(dir) == "" || dir == "." {
		return errors.New("state directory is invalid")
	}
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create state directory: %w", errMkdir)
	}
	if errChmod := os.Chmod(dir, 0o700); errChmod != nil {
		return fmt.Errorf("set state directory permissions: %w", errChmod)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	errDecode := decoder.Decode(&extra)
	if errors.Is(errDecode, io.EOF) {
		return nil
	}
	if errDecode == nil {
		return errors.New("extra JSON value")
	}
	return errDecode
}
