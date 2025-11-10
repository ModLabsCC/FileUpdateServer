package main

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	dataDir        = "data"
	metaFilename   = "meta.json"
	storedFilename = "current.bin"
)

type fileMeta struct {
	OriginalName string    `json:"original_name"`
	Size         int64     `json:"size"`
	UploadedAt   time.Time `json:"uploaded_at"`
}

type fileStore struct {
	mu   sync.RWMutex
	meta *fileMeta
}

func newFileStore() (*fileStore, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}

	fs := &fileStore{}
	if err := fs.loadMeta(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return fs, nil
}

func (fs *fileStore) loadMeta() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	metaPath := filepath.Join(dataDir, metaFilename)
	file, err := os.Open(metaPath)
	if err != nil {
		return err
	}
	defer file.Close()

	var meta fileMeta
	if err := json.NewDecoder(file).Decode(&meta); err != nil {
		return err
	}
	fs.meta = &meta
	return nil
}

func (fs *fileStore) saveFile(r io.Reader, originalName string, size int64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	tmpPath := filepath.Join(dataDir, storedFilename+".tmp")
	targetPath := filepath.Join(dataDir, storedFilename)
	metaPath := filepath.Join(dataDir, metaFilename)

	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	if _, err := io.Copy(tmpFile, r); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	meta := fileMeta{
		OriginalName: originalName,
		Size:         size,
		UploadedAt:   time.Now().UTC(),
	}

	metaFile, err := os.Create(metaPath)
	if err != nil {
		return err
	}

	if err := json.NewEncoder(metaFile).Encode(&meta); err != nil {
		metaFile.Close()
		return err
	}

	if err := metaFile.Close(); err != nil {
		return err
	}

	fs.meta = &meta
	return nil
}

func (fs *fileStore) currentFilePath() (string, bool) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	if fs.meta == nil {
		return "", false
	}
	path := filepath.Join(dataDir, storedFilename)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}
	}
	return path, true
}

func (fs *fileStore) currentMeta() *fileMeta {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	if fs.meta == nil {
		return nil
	}
	metaCopy := *fs.meta
	return &metaCopy
}

func main() {
	password := os.Getenv("FILE_UPLOAD_PASSWORD")
	if password == "" {
		log.Println("warning: FILE_UPLOAD_PASSWORD is not set; uploads will be rejected")
	}

	store, err := newFileStore()
	if err != nil {
		log.Fatalf("failed to initialize file store: %v", err)
	}

	tmpl := template.Must(template.ParseFiles(filepath.Join("templates", "index.html")))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		meta := store.currentMeta()
		data := struct {
			Meta           *fileMeta
			HasFile        bool
			PasswordIsSet  bool
			UploadEndpoint string
			FileEndpoint   string
		}{
			Meta:           meta,
			HasFile:        meta != nil,
			PasswordIsSet:  password != "",
			UploadEndpoint: "/upload",
			FileEndpoint:   "/file",
		}

		if err := tmpl.Execute(w, data); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
			return
		}
	})

	http.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		path, ok := store.currentFilePath()
		if !ok {
			http.NotFound(w, r)
			return
		}

		meta := store.currentMeta()
		if meta != nil && meta.OriginalName != "" {
			w.Header().Set("Content-Disposition", `attachment; filename="`+meta.OriginalName+`"`)
		}
		http.ServeFile(w, r, path)
	})

	http.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if password == "" {
			http.Error(w, "uploads disabled: FILE_UPLOAD_PASSWORD not set", http.StatusForbidden)
			return
		}

		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "invalid form data", http.StatusBadRequest)
			return
		}

		if r.FormValue("password") != password {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		if header.Size == 0 {
			http.Error(w, "file is empty", http.StatusBadRequest)
			return
		}

		if err := store.saveFile(file, header.Filename, header.Size); err != nil {
			log.Printf("failed to save file: %v", err)
			http.Error(w, "failed to save file", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	log.Println("listening on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
