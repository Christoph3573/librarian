package bsb

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ---- borrow: digital download + physical request info ----

// BorrowResult describes what borrow did (or would do with --dry-run).
type BorrowResult struct {
	MMS     string   `json:"mms"`
	Title   string   `json:"title"`
	Mode    string   `json:"mode"` // "download" | "request" | "open-browser" | "search-in-portal" | "request-info" | "offer" | "cancel"
	DryRun  bool     `json:"dry_run"`
	Message string   `json:"message"`
	Files   []string `json:"files,omitempty"`
	// Action is the entitlement-router action taken (download | open-browser |
	// search-in-portal | request-physical | none).
	Action string `json:"action,omitempty"`
	// Electronic holds the classified offers (PNX links + edelivery
	// entitlement) the routing decision was based on.
	Electronic []ElectronicOffer `json:"electronic,omitempty"`
	// Route is the chosen best legal access path (nil when none actionable).
	Route *ElectronicOffer `json:"electronic_route,omitempty"`
	// ElectronicError is set when the edelivery entitlement check failed;
	// routing then fell back to PNX classification only.
	ElectronicError string `json:"electronic_error,omitempty"`
	// Detail holds the titleServices (+ svcId detail) output for
	// physical requests when --detail was given.
	Detail *TitleServices `json:"detail,omitempty"`
}

// MDZID extracts the MDZ object id (bsbXXXXXXXX) from an MDZ resolving URL.
func MDZID(raw string) string {
	m := regexp.MustCompile(`bsb\d+`).FindString(raw)
	return m
}

// ResolveMDZ follows http://mdz-nbn-resolving.de/urn:... to the viewer
// (https://www.digitale-sammlungen.de/view/<id>) and returns the object id.
func ResolveMDZ(client *http.Client, resolvingURL string) (objectID string, err error) {
	noredir := *client
	noredir.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req, _ := http.NewRequest("GET", resolvingURL, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := noredir.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		// already final or direct 200
		loc = resolvingURL
	}
	m := regexp.MustCompile(`bsb\d+`).FindString(loc)
	if m == "" {
		return "", fmt.Errorf("no MDZ object id in redirect %q", loc)
	}
	return m, nil
}

// iiifManifest mirrors the fields of a IIIF presentation v2 manifest we need.
type iiifManifest struct {
	Label     string `json:"label"`
	Sequences []struct {
		Canvases []struct {
			Label  string `json:"label"`
			Images []struct {
				Resource struct {
					Service struct {
						ID string `json:"@id"`
					} `json:"service"`
				} `json:"resource"`
			} `json:"images"`
		} `json:"canvases"`
	} `json:"sequences"`
}

// DownloadMDZ downloads all IIIF page images of an MDZ object into dir and
// returns the file paths. maxPages<=0 means all pages.
func DownloadMDZ(client *http.Client, objectID, dir string, maxPages int, onPage func(done, total int)) ([]string, int, error) {
	mURL := "https://api.digitale-sammlungen.de/iiif/presentation/v2/" + objectID + "/manifest"
	req, _ := http.NewRequest("GET", mURL, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch IIIF manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("fetch IIIF manifest: HTTP %d", resp.StatusCode)
	}
	var m iiifManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, 0, fmt.Errorf("decode IIIF manifest: %w", err)
	}
	if len(m.Sequences) == 0 {
		return nil, 0, fmt.Errorf("IIIF manifest has no sequences")
	}
	canvases := m.Sequences[0].Canvases
	total := len(canvases)
	if maxPages > 0 && maxPages < total {
		canvases = canvases[:maxPages]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, total, err
	}
	dl := &http.Client{Timeout: 60 * time.Second}
	var files []string
	for i, cv := range canvases {
		if len(cv.Images) == 0 {
			continue
		}
		svc := cv.Images[0].Resource.Service.ID
		imgURL := svc + "/full/max/0/default.jpg"
		name := fmt.Sprintf("%s_%05d.jpg", objectID, i+1)
		dst := filepath.Join(dir, name)
		if err := downloadFile(dl, imgURL, dst); err != nil {
			return files, total, fmt.Errorf("page %d: %w", i+1, err)
		}
		files = append(files, dst)
		if onPage != nil {
			onPage(len(files), total)
		}
	}
	return files, total, nil
}

func downloadFile(client *http.Client, url, dst string) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, io.LimitReader(resp.Body, 500<<20))
	return err
}

// IsMDZLink reports whether a delivery link points at the MDZ resolver.
func IsMDZLink(raw string) bool {
	return strings.Contains(raw, "mdz-nbn-resolving.de")
}

// IsFreeFulltext is deprecated: the display label ("Volltext") also labels
// portal/collection pages, so label sniffing produced false-positive
// downloads. Use ClassifyLink (KindOpen) via the entitlement router instead.
func IsFreeFulltext(l DeliveryLink) bool {
	if l.LinkType == "linktorsrc" {
		return true
	}
	lbl := strings.ToLower(l.DisplayLabel)
	return strings.Contains(lbl, "volltext") || strings.Contains(lbl, "kostenfrei") ||
		strings.Contains(lbl, "full text") || strings.Contains(lbl, "open access")
}

// DownloadDirect fetches a plain URL (e.g. DNB Inhaltstext, TOC PDF) into dir.
func DownloadDirect(client *http.Client, rawURL, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	base := filepath.Base(u.Path)
	if base == "" || base == "/" || !strings.Contains(base, ".") {
		base = "download"
	}
	dst := filepath.Join(dir, base)
	if err := downloadFile(client, rawURL, dst); err != nil {
		return "", err
	}
	return dst, nil
}
