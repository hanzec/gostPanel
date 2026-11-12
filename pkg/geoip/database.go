package geoip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

const (
	DefaultDatabasePath = "./data/server-country.mmdb"
	defaultDatabaseURL  = "https://github.com/sapics/ip-location-db/releases/download/latest/server-country.mmdb"
	maxDatabaseSize     = 32 << 20
)

var ErrDatabaseNotReady = errors.New("GeoIP database is not ready")

type Database struct {
	path        string
	downloadURL string
	reader      atomic.Pointer[maxminddb.Reader]
}

func NewDatabase(path, downloadURL string) *Database {
	downloadURL = strings.TrimSpace(downloadURL)
	if downloadURL == "" {
		downloadURL = defaultDatabaseURL
	}
	return &Database{path: path, downloadURL: downloadURL}
}

// Load makes a validated local database available without making a network request.
func (db *Database) Load() error {
	data, err := os.ReadFile(db.path)
	if err != nil {
		return err
	}
	reader, err := openDatabase(data)
	if err != nil {
		return err
	}
	db.reader.Store(reader)
	return nil
}

// Refresh downloads a new database while lookups continue using the current one.
func (db *Database) Refresh(ctx context.Context) error {
	data, err := downloadDatabase(ctx, db.downloadURL)
	if err != nil {
		return err
	}
	reader, err := openDatabase(data)
	if err != nil {
		return err
	}
	if err := saveDatabase(db.path, data); err != nil {
		return err
	}
	db.reader.Store(reader)
	return nil
}

func (db *Database) Ready() bool {
	return db.reader.Load() != nil
}

// Country only reads the loaded database; downloads never delay a lookup.
func (db *Database) Country(ctx context.Context, ip netip.Addr) (string, error) {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Zone() != "" {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	reader := db.reader.Load()
	if reader == nil {
		return "", ErrDatabaseNotReady
	}
	var code string
	if err := reader.Lookup(ip).DecodePath(&code, "country", "iso_code"); err != nil {
		return "", err
	}
	if !isCountryCode(code) {
		return "", nil
	}
	return code, nil
}

func openDatabase(data []byte) (*maxminddb.Reader, error) {
	reader, err := maxminddb.OpenBytes(data)
	if err != nil {
		return nil, fmt.Errorf("open GeoIP database: %w", err)
	}
	if err := reader.Verify(); err != nil {
		return nil, fmt.Errorf("verify GeoIP database: %w", err)
	}
	return reader, nil
}

func downloadDatabase(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download GeoIP database: HTTP %s", res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxDatabaseSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDatabaseSize {
		return nil, fmt.Errorf("GeoIP database exceeds %d bytes", maxDatabaseSize)
	}
	return data, nil
}

func saveDatabase(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".geoip-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
