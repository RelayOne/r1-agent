package server

// csv_export.go — A5 admin panel CSV streaming helper.
//
// The billing route exposes ?format=csv. encoding/csv.Writer plus a
// flush-per-row pattern keeps memory constant when an operator pulls
// a 100k-row export.
//
// Spec: specs/admin-panel.md §10 task 20.

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"time"
)

// CSVStream writes a CSV stream to w with the correct headers for an
// attachment download. The header slice becomes the first row; the
// next() callback is invoked repeatedly until it returns (nil, false).
// Each row is flushed individually so the response trickles out
// instead of buffering in memory.
//
// The filename "<prefix>-YYYY-MM-DD.csv" is computed against now()
// using UTC so test fixtures and operator downloads match the spec's
// Content-Disposition contract.
func CSVStream(w http.ResponseWriter, filenamePrefix string, header []string, next func() ([]string, bool)) error {
	if filenamePrefix == "" {
		filenamePrefix = "export"
	}
	stamp := time.Now().UTC().Format("2006-01-02")
	disposition := fmt.Sprintf(`attachment; filename=%q`, filenamePrefix+"-"+stamp+".csv")

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Cache-Control", "no-store")
	// Allow streaming through proxies without buffering the whole body.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return fmt.Errorf("csv header: %w", err)
	}
	cw.Flush()
	flushHTTP(w)
	if err := cw.Error(); err != nil {
		return err
	}

	for {
		row, ok := next()
		if !ok {
			break
		}
		if err := cw.Write(row); err != nil {
			return fmt.Errorf("csv row: %w", err)
		}
		cw.Flush()
		flushHTTP(w)
		if err := cw.Error(); err != nil {
			return err
		}
	}
	return nil
}

// CSVStreamWithStamp lets callers override the date stamp (used by
// the billing handler which names its file by the report month, not
// by today's date). The filename becomes "<prefix>-<stamp>.csv".
func CSVStreamWithStamp(w http.ResponseWriter, filenamePrefix, stamp string, header []string, next func() ([]string, bool)) error {
	if filenamePrefix == "" {
		filenamePrefix = "export"
	}
	if stamp == "" {
		stamp = time.Now().UTC().Format("2006-01-02")
	}
	disposition := fmt.Sprintf(`attachment; filename=%q`, filenamePrefix+"-"+stamp+".csv")

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return fmt.Errorf("csv header: %w", err)
	}
	cw.Flush()
	flushHTTP(w)

	for {
		row, ok := next()
		if !ok {
			break
		}
		if err := cw.Write(row); err != nil {
			return fmt.Errorf("csv row: %w", err)
		}
		cw.Flush()
		flushHTTP(w)
		if err := cw.Error(); err != nil {
			return err
		}
	}
	return nil
}

// flushHTTP invokes the underlying http.Flusher when present so the
// CSV bytes leave the kernel buffer per row. ResponseRecorders in
// tests don't implement Flusher — that's fine, the type assertion is
// a no-op there.
func flushHTTP(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
