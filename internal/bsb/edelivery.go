package bsb

import (
	"net/url"
	"strings"
)

// ---- electronic delivery (licensed e-books / e-journals) ----
//
// PNX delivery links are NOT download URLs. For Alma-E records they usually
// point at package portals (DBIS collection pages, Springer package search,
// EBSCO login walls) rather than at the title itself. Treating them as PDFs
// is wrong (false-positive downloads, login walls).
//
// The authoritative source for "can this user read this title online" is the
// edelivery endpoint (seen in the HAR capture):
//
//	POST /primaws/rest/pub/edelivery/alma<mms>?vid=..&lang=..&googleScholar=false
//	body: {"sharedDigitalCandidates":null}
//	→ { electronicServices: [{ packageName, hasAccess, licenceExist,
//	    serviceUrl: "/view/action/uresolver.do?operation=resolveService&…",
//	    availiability, authNote, … }] }
//
// It is a pub endpoint (anonymous OK) but user-aware: with a login JWT the
// hasAccess flag reflects the patron's entitlements. hasAccess=false means
// "licensed, but not for you" — the honest fallback is the physical /
// resource-sharing chain, not a download attempt.
//
// This file therefore implements an entitlement router, not a downloader:
// PNX links are classified into kinds, edelivery answers entitlement, and
// every offer carries the best legal action (download | open-browser |
// search-in-portal | request-physical | none). Publisher SSO/DRM/JS viewers
// are never automated — licensed title access is handed to the browser.

// EdeliveryService is one licensed electronic service for a record.
type EdeliveryService struct {
	AdaptorID        string `json:"adaptorid"`
	IlsAPIID         string `json:"ilsApiId"`
	ServiceURL       string `json:"serviceUrl"`
	LicenceExist     string `json:"licenceExist"`
	PackageName      string `json:"packageName"`
	Availability     string `json:"availiability"`
	AuthNote         string `json:"authNote"`
	PublicNote       string `json:"publicNote"`
	HasAccess        bool   `json:"hasAccess"`
	ServiceType      string `json:"serviceType"`
	ContextServiceID string `json:"contextServiceId"`
}

// EdeliveryResponse mirrors the parts of the edelivery reply we use.
type EdeliveryResponse struct {
	ElectronicServices    []EdeliveryService `json:"electronicServices"`
	ServiceMode           []string           `json:"serviceMode"`
	Availability          []string           `json:"availability"`
	DisplayedAvailability string             `json:"displayedAvailability"`
}

// Edelivery fetches the licensed electronic services for a record.
// Anonymous OK (pub endpoint); with a login JWT hasAccess reflects the
// patron's entitlements.
func (c *Client) Edelivery(mms string) (*EdeliveryResponse, error) {
	var out EdeliveryResponse
	q := url.Values{"googleScholar": {"false"}}
	body := map[string]any{"sharedDigitalCandidates": nil}
	if err := c.post("/primaws/rest/pub/edelivery/alma"+mms, q, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResolverURL expands a serviceUrl (usually site-relative
// "/view/action/uresolver.do?...") to an absolute URL.
func (s EdeliveryService) ResolverURL() string {
	if strings.HasPrefix(s.ServiceURL, "/") {
		return BaseURL + s.ServiceURL
	}
	return s.ServiceURL
}

// ---- link classification ----

// ElectronicKind classifies what a delivery link actually points at.
type ElectronicKind string

const (
	// KindOpen is freely downloadable content (MDZ scans, open repositories,
	// direct PDFs). The CLI may download these itself.
	KindOpen ElectronicKind = "open"
	// KindResolver is a per-title resolver (uresolver.do, DOI, NBN) that
	// lands on the title after SSO/entitlement checks. Browser action.
	KindResolver ElectronicKind = "resolver"
	// KindPackagePortal is a collection/database landing page, not the title
	// (DBIS resource page, Springer package search, EBSCO login wall).
	// The user must search the title inside the portal.
	KindPackagePortal ElectronicKind = "package-portal"
	// KindAggregatorTitle is a title-level page on a licensed aggregator
	// (Ebook Central, EBSCO, Springer book/chapter page) behind SSO/DRM.
	// Browser action, no CLI download.
	KindAggregatorTitle ElectronicKind = "aggregator-title"
	// KindLicensedService is an edelivery electronicService entry: the
	// entitlement (hasAccess) is authoritative, access goes via resolverUrl.
	KindLicensedService ElectronicKind = "licensed-service"
	// KindUnknown is anything else — never treated as a download.
	KindUnknown ElectronicKind = "unknown"
)

// portalMarkers are lowercase URL substrings identifying collection/portal
// pages rather than title pages.
var portalMarkers = []string{
	"dbis.ur.de/resources",
	"springer.com/search",
	"package=",
	"sciencedirect.com/science/books",
	"sciencedirect.com/science/journal",
	"sciencedirect.com/search",
	"ebscohost.com/login.aspx",
	"degruyterbrill.com/serial",
	"login.aspx",
	"/search?",
}

// aggregatorHosts are licensed platforms whose deep links address a title
// but stay behind SSO/DRM.
var aggregatorHosts = []string{
	"ebookcentral.proquest.com",
	"ebooks.ebscohost.com",
	"dl.acm.org",
	"ieeexplore.ieee.org",
	"tandfonline.com",
	"jstor.org",
	"cambridge.org",
	"academic.oup.com",
}

// ClassifyLink decides what a PNX delivery link points at. It never trusts
// the display label ("Volltext" also labels portal pages).
func ClassifyLink(l DeliveryLink) ElectronicKind {
	raw := strings.ToLower(strings.TrimSpace(l.LinkURL))
	if raw == "" {
		return KindUnknown
	}
	// Freely downloadable hosts first.
	if IsMDZLink(l.LinkURL) || strings.Contains(raw, "digitale-sammlungen.de") {
		return KindOpen
	}
	if strings.Contains(raw, "arxiv.org") && strings.Contains(raw, "/pdf/") {
		return KindOpen
	}
	if strings.HasSuffix(strings.Split(raw, "?")[0], ".pdf") {
		return KindOpen
	}
	// Per-title resolvers.
	if strings.Contains(raw, "uresolver.do") || strings.Contains(raw, "resolveservice") ||
		strings.Contains(raw, "sfx") || strings.Contains(raw, "doi.org/") ||
		strings.Contains(raw, "nbn-resolving") || strings.Contains(raw, "d-nb.info/") {
		return KindResolver
	}
	// Package portals (collection pages, login walls, package searches).
	for _, m := range portalMarkers {
		if strings.Contains(raw, m) {
			return KindPortalOrTitle(raw)
		}
	}
	// Licensed aggregators.
	host := linkHost(l.LinkURL)
	for _, h := range aggregatorHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return KindAggregatorTitle
		}
	}
	// Title-level pages on the big commercial publishers are aggregator
	// titles; their roots/search pages are portals.
	switch host {
	case "link.springer.com", "www.sciencedirect.com", "sciencedirect.com",
		"www.degruyterbrill.com", "degruyterbrill.com", "books.rsc.org",
		"platform.almanhal.com", "search.ebscohost.com":
		return KindPortalOrTitle(raw)
	}
	return KindUnknown
}

// KindPortalOrTitle splits a known platform host into portal (collection
// root, search, login) vs. aggregator title (deep title path).
func KindPortalOrTitle(raw string) ElectronicKind {
	u, err := url.Parse(raw)
	if err != nil {
		return KindPackagePortal
	}
	p := strings.ToLower(u.Path)
	if p == "" || p == "/" || strings.Contains(raw, "login.aspx") ||
		strings.Contains(raw, "/search") || strings.Contains(raw, "package=") ||
		strings.Contains(p, "/resources") || strings.Contains(p, "/serial") ||
		strings.Contains(p, "/books/sub") {
		return KindPackagePortal
	}
	return KindAggregatorTitle
}

func linkHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// PlatformName derives a human platform label from a link URL host.
func PlatformName(rawURL string) string {
	h := linkHost(rawURL)
	if h == "" {
		return "unknown"
	}
	return h
}

// ---- offers + routing ----

// Electronic actions. Only "download" is executed by the CLI itself;
// everything else is handed to the browser/portal or to the physical chain.
const (
	ActionDownload        = "download"
	ActionOpenBrowser     = "open-browser"
	ActionSearchInPortal  = "search-in-portal"
	ActionRequestPhysical = "request-physical"
	ActionNone            = "none"
)

// ElectronicOffer is one access path for a title: where it lives, whether
// this user may read it, and the best legal action to get there.
type ElectronicOffer struct {
	Platform    string `json:"platform"`
	Kind        string `json:"kind"`
	HasAccess   *bool  `json:"has_access,omitempty"`
	Licence     string `json:"licence,omitempty"`
	Package     string `json:"package,omitempty"`
	ResolverURL string `json:"resolver_url,omitempty"`
	DirectURL   string `json:"direct_url,omitempty"`
	Label       string `json:"label,omitempty"`
	Action      string `json:"action"`
	Reason      string `json:"reason,omitempty"`
}

func boolPtr(b bool) *bool { return &b }

// ElectronicAccess builds the offer list for a record: classified PNX links
// plus authoritative edelivery entitlement entries. ed may be nil (fetch
// failed or record is not electronic) — PNX classification still applies.
func ElectronicAccess(doc *Doc, ed *EdeliveryResponse) []ElectronicOffer {
	var offers []ElectronicOffer
	for _, l := range doc.OnlineLinks() {
		if l.LinkType == "addlink" || strings.TrimSpace(l.LinkURL) == "" {
			continue // record metadata formats (MARC/RDF), not content
		}
		kind := ClassifyLink(l)
		o := ElectronicOffer{
			Platform:  PlatformName(l.LinkURL),
			Kind:      string(kind),
			DirectURL: l.LinkURL,
			Label:     l.DisplayLabel,
		}
		switch kind {
		case KindOpen:
			o.Action = ActionDownload
			o.Reason = "free full text, safe to download"
		case KindResolver:
			o.Action = ActionOpenBrowser
			o.ResolverURL = l.LinkURL
			o.Reason = "per-title resolver, entitlement checked in browser (SSO)"
		case KindPackagePortal:
			o.Action = ActionSearchInPortal
			o.Reason = "collection/portal page, not the title — search the title inside the portal"
		case KindAggregatorTitle:
			o.Action = ActionOpenBrowser
			o.ResolverURL = l.LinkURL
			o.Reason = "licensed aggregator title (SSO/DRM), open in browser"
		default:
			o.Action = ActionOpenBrowser
			o.ResolverURL = l.LinkURL
			o.Reason = "unclassified link, never treated as a download — inspect in browser"
		}
		offers = append(offers, o)
	}
	if ed != nil {
		for _, s := range ed.ElectronicServices {
			o := ElectronicOffer{
				Platform:    s.PackageName,
				Kind:        string(KindLicensedService),
				HasAccess:   boolPtr(s.HasAccess),
				Package:     s.PackageName,
				ResolverURL: s.ResolverURL(),
			}
			if s.LicenceExist != "" {
				o.Licence = s.LicenceExist
			}
			if s.PublicNote != "" {
				o.Label = s.PublicNote
			}
			if s.HasAccess {
				o.Action = ActionOpenBrowser
				o.Reason = "licensed for this user, open resolver link in browser (publisher SSO)"
			} else {
				o.Action = ActionRequestPhysical
				o.Reason = "licensed, but no access for this user — fall back to physical loan / Fernleihe"
			}
			if o.Platform == "" {
				o.Platform = "unknown"
			}
			offers = append(offers, o)
		}
	}
	return offers
}

// BestElectronicRoute picks the best legal access path: free downloads
// first, then entitled licensed access, then portal search, then the
// physical fallback. Returns nil when there is nothing actionable.
func BestElectronicRoute(offers []ElectronicOffer) *ElectronicOffer {
	var entitled, portal, physical *ElectronicOffer
	var unknownBrowser *ElectronicOffer
	for i := range offers {
		o := &offers[i]
		switch o.Action {
		case ActionDownload:
			return o
		case ActionOpenBrowser:
			if o.HasAccess != nil && *o.HasAccess {
				if entitled == nil {
					entitled = o
				}
				continue
			}
			if o.HasAccess == nil && unknownBrowser == nil {
				unknownBrowser = o
			}
		case ActionSearchInPortal:
			if portal == nil {
				portal = o
			}
		case ActionRequestPhysical:
			if physical == nil {
				physical = o
			}
		}
	}
	if entitled != nil {
		return entitled
	}
	// Licensed-but-denied beats guessing: physical fallback is more useful
	// than an unclassified browser link, but an entitled resolver and any
	// concrete portal/search hint beat the fallback.
	if unknownBrowser != nil {
		return unknownBrowser
	}
	if portal != nil {
		return portal
	}
	if physical != nil {
		return physical
	}
	return nil
}

// ResolveElectronic fetches edelivery (best effort) for electronic-looking
// records and returns the full offer list. Pure physical records without
// online links skip the network call and yield nil. edErr is non-empty when
// the edelivery fetch failed — callers surface it instead of failing.
func ResolveElectronic(client *Client, doc *Doc) (offers []ElectronicOffer, edErr string) {
	var ed *EdeliveryResponse
	if isElectronicRecord(doc) {
		if e, err := client.Edelivery(doc.MMS()); err != nil {
			edErr = err.Error()
		} else {
			ed = e
		}
	}
	return ElectronicAccess(doc, ed), edErr
}

func isElectronicRecord(doc *Doc) bool {
	for _, c := range doc.Delivery.DeliveryCategory {
		if c == "Alma-E" || c == "Alma-D" {
			return true
		}
	}
	for _, m := range doc.Delivery.ServiceMode {
		if m == "Viewit" {
			return true
		}
	}
	return len(doc.OnlineLinks()) > 0
}

// HasOpenLink reports whether the record carries a freely downloadable link.
func HasOpenLink(doc *Doc) bool {
	for _, l := range doc.OnlineLinks() {
		if ClassifyLink(l) == KindOpen {
			return true
		}
	}
	return false
}
