package bsb

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ---- search ----

// SearchParams controls a catalog search.
type SearchParams struct {
	// Query is the free-text query, e.g. "kafka faust".
	Query string
	// Field is the Primo search field, e.g. "any", "title", "creator".
	// Empty defaults to "any".
	Field string
	// Limit caps the number of records (server max ~50).
	Limit int
	// Offset is the paging offset.
	Offset int
	// WithDelivery fetches availability info (skipDelivery=N).
	// Slower; anonymous users get it too.
	WithDelivery bool
	// Lang overrides the client language for this call.
	Lang string
	// IncludeFacets are Primo facet filters, e.g. "rtype:books",
	// "tlevel:open_access", "creationdate:2020". They are sent as
	// qInclude=facet_<name>,exact,<value> joined by "|,|" (see HAR).
	IncludeFacets []string
	// ExcludeFacets are Primo facet exclusions, same "name:value" syntax,
	// sent as qExclude.
	ExcludeFacets []string
	// WithFacets requests the facet aggregation + highlights in the response
	// (getMore=0 call, as in the HAR example).
	WithFacets bool
}

// StringList tolerates fields that are sometimes a JSON string, sometimes a
// list of strings (Primo is inconsistent here, e.g. control/sourceid).
type StringList []string

// UnmarshalJSON accepts "x" or ["x", ...].
func (s *StringList) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = []string{single}
		return nil
	}
	var multi []string
	if err := json.Unmarshal(data, &multi); err != nil {
		return err
	}
	*s = multi
	return nil
}

// FacetValue is one bucket of a search facet (e.g. value "books", 64713 hits).
type FacetValue struct {
	Value string `json:"value"`
	Count any    `json:"count"`
}

// Facet is one search facet (rtype, tlevel, creator, creationdate, ...).
type Facet struct {
	Name   string       `json:"name"`
	Values []FacetValue `json:"values"`
}

// SearchResponse mirrors the relevant parts of the PNX JSON.
type SearchResponse struct {
	Info struct {
		Total             int `json:"total"`
		TotalResultsLocal int `json:"totalResultsLocal"`
		TotalResultsPC    int `json:"totalResultsPC"`
		First             int `json:"first"`
		Last              int `json:"last"`
	} `json:"info"`
	Docs []Doc `json:"docs"`
	// Facets holds the aggregation buckets (only when WithFacets).
	Facets []Facet `json:"facets"`
	// Highlights holds matched-term snippets per field (only with WithFacets).
	Highlights map[string][]string `json:"highlights"`
}

// Doc is a single search hit: bibliographic data (PNX) plus delivery info.
type Doc struct {
	Context  string   `json:"context"`
	Adaptor  string   `json:"adaptor"`
	ID       string   `json:"@id"`
	Pnx      Pnx      `json:"pnx"`
	Delivery Delivery `json:"delivery"`
}

// Pnx holds display/control/addata sections.
type Pnx struct {
	Display struct {
		Type         []string `json:"type"`
		Title        []string `json:"title"`
		Creator      []string `json:"creator"`
		Contributor  []string `json:"contributor"`
		CreationDate []string `json:"creationdate"`
		Publisher    []string `json:"publisher"`
		Format       []string `json:"format"`
		Language     []string `json:"language"`
		Identifier   []string `json:"identifier"`
		MMS          []string `json:"mms"`
		Subject      []string `json:"subject"`
		Description  []string `json:"description"`
		IsPartOf     []string `json:"ispartof"`
	} `json:"display"`
	Control struct {
		RecordID []string   `json:"recordid"`
		SourceID StringList `json:"sourceid"`
		// SourceRecordID is pnx.control.sourcerecordid[0], needed as
		// sourceRecordId for getPhysicalService and as localMmsId fallback.
		SourceRecordID []string `json:"sourcerecordid"`
	} `json:"control"`
	Addata struct {
		ISBN   []string `json:"isbn"`
		ISSN   []string `json:"issn"`
		DOI    []string `json:"doi"`
		Genre  []string `json:"genre"`
		Format []string `json:"format"`
	} `json:"addata"`
}

// Holding is one physical copy/location.
type Holding struct {
	LibraryCode        string `json:"libraryCode"`
	MainLocation       string `json:"mainLocation"`
	SubLocation        string `json:"subLocation"`
	CallNumber         string `json:"callNumber"`
	AvailabilityStatus string `json:"availabilityStatus"`
	// IlsAPIID + HoldID identify the record/holding for ILS calls
	// (getPhysicalService, titleServices).
	IlsAPIID     string `json:"ilsApiId"`
	HoldID       string `json:"holdId"`
	HolKey       string `json:"holKey"`
	Organization string `json:"organization"`
}

// DeliveryLink is an online link (full text, TOC, thumbnail, ...).
type DeliveryLink struct {
	LinkType     string `json:"linkType"`
	LinkURL      string `json:"linkURL"`
	DisplayLabel string `json:"displayLabel"`
}

// GetItLink is a physical request/service entry point.
type GetItLink struct {
	Category     string `json:"category"`
	GetItTabText string `json:"-"`
	Links        []struct {
		GetItTabText string `json:"getItTabText"`
		Link         string `json:"link"`
		IlsAPIID     string `json:"ilsApiId"`
	} `json:"links"`
}

// Delivery bundles availability + services for a record.
type Delivery struct {
	DeliveryCategory []string       `json:"deliveryCategory"`
	Availability     []string       `json:"availability"`
	Holding          []Holding      `json:"holding"`
	BestLocation     *Holding       `json:"bestlocation"`
	Link             []DeliveryLink `json:"link"`
	GetIt1           []GetItLink    `json:"GetIt1"`
	// ServiceMode drives the web UI render path ("ovp", "howovp"+Viewit,
	// "Viewit", ...). For a physical loan we need "ovp".
	ServiceMode []string `json:"serviceMode"`
	// PhysicalServiceID is filled by GET /pub/getPhysicalService/<ilsId>.
	// It selects the titleServices detail level (svcId) whose serviceinfo
	// carries the per-type "link-to-service" submit URLs.
	PhysicalServiceID string `json:"physicalServiceId"`
	// RecordOwner is the Alma institution owning the record (49BVB_BSB),
	// sent as recordOwner to getPhysicalService.
	RecordOwner string `json:"recordOwner"`
}

// ResourceType returns pnx.display.type[0] ("book", "journal", ...), used
// as resource_type for getPhysicalService and resourceType for bestoffer.
func (d *Doc) ResourceType() string {
	if len(d.Pnx.Display.Type) > 0 {
		return d.Pnx.Display.Type[0]
	}
	return ""
}

// SourceRecordID returns pnx.control.sourcerecordid[0] for getPhysicalService.
func (d *Doc) SourceRecordID() string {
	if len(d.Pnx.Control.SourceRecordID) > 0 {
		return d.Pnx.Control.SourceRecordID[0]
	}
	return ""
}

// MMS returns the Alma MMS ID of the record.
func (d *Doc) MMS() string {
	if len(d.Pnx.Display.MMS) > 0 {
		return d.Pnx.Display.MMS[0]
	}
	return ""
}

// Title returns the main title.
func (d *Doc) Title() string {
	if len(d.Pnx.Display.Title) > 0 {
		return strings.TrimSpace(d.Pnx.Display.Title[0])
	}
	return "(ohne Titel)"
}

// OnlineLinks returns non-thumbnail delivery links (full text etc.).
func (d *Doc) OnlineLinks() []DeliveryLink {
	var out []DeliveryLink
	for _, l := range d.Delivery.Link {
		if l.LinkType == "thumbnail" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// facetParam converts "name:value" into Primo's qInclude/qExclude syntax
// "facet_<name>,exact,<value>". Invalid entries are skipped.
func facetParam(f string) string {
	name, value, ok := strings.Cut(f, ":")
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if !ok || name == "" || value == "" {
		return ""
	}
	return "facet_" + name + ",exact," + value
}

// Search runs a catalog search (anonymous OK). The q parameter follows Primo
// syntax "<field>,contains,<terms>"; terms with commas are sanitized.
// Facet filters follow the HAR format: qInclude=facet_rtype,exact,books
// (multiple joined with "|,|").
func (c *Client) Search(p SearchParams) (*SearchResponse, error) {
	field := p.Field
	if field == "" {
		field = "any"
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 10
	}
	skip := "Y"
	if p.WithDelivery {
		skip = "N"
	}
	lang := c.Lang
	if p.Lang != "" {
		lang = p.Lang
	}
	q := url.Values{
		"q":              {field + ",contains," + strings.ReplaceAll(p.Query, ",", " ")},
		"inst":           {Institution},
		"scope":          {DefaultScope},
		"tab":            {DefaultTab},
		"lang":           {lang},
		"limit":          {fmt.Sprint(limit)},
		"offset":         {fmt.Sprint(p.Offset)},
		"sort":           {"rank"},
		"skipDelivery":   {skip},
		"pcAvailability": {"false"},
		"vid":            {c.View},
	}
	var inc, exc []string
	for _, f := range p.IncludeFacets {
		if s := facetParam(f); s != "" {
			inc = append(inc, s)
		}
	}
	for _, f := range p.ExcludeFacets {
		if s := facetParam(f); s != "" {
			exc = append(exc, s)
		}
	}
	if len(inc) > 0 {
		q.Set("qInclude", strings.Join(inc, "|,|"))
	}
	if len(exc) > 0 {
		q.Set("qExclude", strings.Join(exc, "|,|"))
	}
	if p.WithFacets {
		// getMore=0 returns facet aggregation + highlights (see HAR).
		q.Set("getMore", "0")
		q.Set("rtaLinks", "true")
	}
	var res SearchResponse
	if err := c.do("GET", "/primaws/rest/pub/pnxs", q, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// SearchByMMS fetches a record directly via its Alma MMS ID.
func (c *Client) SearchByMMS(mms string, withDelivery bool) (*Doc, error) {
	res, err := c.Search(SearchParams{Query: mms, Limit: 3, WithDelivery: withDelivery})
	if err != nil {
		return nil, err
	}
	for i := range res.Docs {
		if res.Docs[i].MMS() == mms {
			return &res.Docs[i], nil
		}
	}
	if len(res.Docs) > 0 {
		return &res.Docs[0], nil
	}
	return nil, fmt.Errorf("no record found for MMS %s", mms)
}
