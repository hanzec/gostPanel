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
	"golang.org/x/sync/singleflight"
)

const (
	DefaultDatabasePath = "./data/server-country.mmdb"
	defaultDatabaseURL  = "https://github.com/sapics/ip-location-db/releases/download/latest/server-country.mmdb"
	maxDatabaseSize     = 32 << 20
)

type Database struct {
	path        string
	downloadURL string

	loading singleflight.Group
	reader  atomic.Pointer[maxminddb.Reader]
}

func NewDatabase(path, downloadURL string) *Database {
	downloadURL = strings.TrimSpace(downloadURL)
	if downloadURL == "" {
		downloadURL = defaultDatabaseURL
	}
	return &Database{path: path, downloadURL: downloadURL}
}

// Country returns the country code for a public address, downloading the database on first use if needed.
func (db *Database) Country(ctx context.Context, ip netip.Addr) (string, error) {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Zone() != "" {
		return "", nil
	}
	reader, err := db.load(ctx)
	if err != nil {
		return "", err
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

func (db *Database) load(ctx context.Context) (*maxminddb.Reader, error) {
	if reader := db.reader.Load(); reader != nil {
		return reader, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A caller can stop waiting while the shared first download continues.
	loaded := db.loading.DoChan("country", func() (any, error) {
		if db.reader.Load() != nil {
			return nil, nil
		}
		reader, err := db.open(context.WithoutCancel(ctx))
		if err == nil {
			db.reader.Store(reader)
		}
		return nil, err
	})
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case result := <-loaded:
		return db.reader.Load(), result.Err
	case <-wait.Done():
		return nil, wait.Err()
	}
}

func (db *Database) open(ctx context.Context) (*maxminddb.Reader, error) {
	data, err := os.ReadFile(db.path)
	missing := errors.Is(err, os.ErrNotExist)
	if missing {
		data, err = downloadDatabase(ctx, db.downloadURL)
	}
	if err != nil {
		return nil, err
	}
	reader, err := maxminddb.OpenBytes(data)
	if err != nil {
		return nil, fmt.Errorf("open GeoIP database: %w", err)
	}
	if err := reader.Verify(); err != nil {
		return nil, fmt.Errorf("verify GeoIP database: %w", err)
	}
	if missing {
		if err := saveDatabase(db.path, data); err != nil {
			return nil, err
		}
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
