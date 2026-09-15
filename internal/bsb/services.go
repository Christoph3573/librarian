package bsb

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ---- title services (request options for a physical record) ----

// TitleService describes one request/service option for a record, e.g.
// reading-room order ("Lesesaalbestellung"), loan ("Ausleihe"), document
// delivery ("Dokumentlieferung").
//
// For physical (OVP) services the browser fetches the "link-to-service" URL
// (GET) to obtain the request form ("services-arr"), then POSTs the filled
// form back to the same URL (plus "/item/<itemId>" when item-level and
// "&lang=.."). See bundle.js PrmRequestServices / request form submit.
type TitleService struct {
	Type        string `json:"type"`
	ServiceType string `json:"service-type"`
	Allowed     string `json:"allowed"`
	Link        string `json:"link-to-service"`
	PublicNote  string `json:"publicNote"`
}

// SubmitURL builds the final POST target for a request form: the service's
// link-to-service, with "/item/<itemID>" appended for item-level requests,
// plus "&lang=<lang>".
func (s TitleService) SubmitURL(itemID, lang string) string {
	u := s.Link
	if itemID != "" {
		u += "/item/" + itemID
	}
	if lang == "" {
		lang = "de"
	}
	if strings.Contains(u, "?") {
		return u + "&lang=" + lang
	}
	return u + "?lang=" + lang
}

// TitleLocation is one location with its call number and items.
type TitleLocation struct {
	MainLocation string `json:"main-location"`
	SubLocation  string `json:"sub-location"`
	CallNumber   string `json:"call-number"`
	SubLocCode   string `json:"sub-location-code"`
	LibraryCode  string `json:"library-code"`
	ItemsSize    int    `json:"items-size"`
	HoldID       string `json:"hold-id"`
	IlsAPIID     string `json:"ils-api-id"`
	org          string
	// AvailabilityStatement is the Alma copy summary, e.g.
	// "(1 Exemplar, 1 verfügbar, 0 Bestellungen)".
	AvailabilityStatement string `json:"availabilityStatement"`
	// Summaries holds the MARC holdings location fields (loc.summary,
	// loc.notes), e.g. journal year/volume coverage
	// ("10.1899 - 91.1940; ...", "(Ab 453.1995 Teilung in Pt. 1 - 3)").
	Summaries map[string][]string `json:"summaries"`
	Items     []TitleItem         `json:"items"`
}

// UnmarshalJSON flattens the nested holdings/location-fields structure of the
// titleServices response into Summaries and keeps organization.
func (l *TitleLocation) UnmarshalJSON(data []byte) error {
	type alias TitleLocation // avoid recursion
	var raw struct {
		alias
		Organization string `json:"organization"`
		Holdings     []struct {
			LocationFields map[string][]string `json:"location-fields"`
		} `json:"holdings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*l = TitleLocation(raw.alias)
	l.org = raw.Organization
	l.Summaries = map[string][]string{}
	for _, h := range raw.Holdings {
		for k, v := range h.LocationFields {
			l.Summaries[k] = append(l.Summaries[k], v...)
		}
	}
	return nil
}

// TitleItem is a single copy.
type TitleItem struct {
	Barcode     string `json:"barcode"`
	Status      string `json:"status"`
	Policy      string `json:"policy"`
	Requestable string `json:"requestable"`
	Location    string `json:"location"`
	CallNumber  string `json:"call-number"`
	DueDate     string `json:"due-date"`
}

// PossibleFilters lists the selectable years/volumes/descriptions for a
// journal-style multi-holding record (see titleServices HAR with svcId).
type PossibleFilters struct {
	Years        []string `json:"years"`
	Volumes      []string `json:"volumes"`
	Descriptions []string `json:"descriptions"`
}

// TitleServices is the response of the titleServices endpoint.
type TitleServices struct {
	ServiceID string          `json:"serviceId"`
	Services  []TitleService  `json:"serviceinfo"`
	Locations []TitleLocation `json:"locations"`
	// Filters holds the year/volume selectors (detail level only).
	Filters PossibleFilters `json:"possible_filters"`
	// RapidoServices holds resource-sharing options (detail level only).
	RapidoServices []TitleService `json:"rapido_services"`
}

type titleServicesRaw struct {
	ReplyText string `json:"reply-text"`
	Data      struct {
		ServiceID string          `json:"serviceId"`
		Services  []TitleService  `json:"serviceinfo"`
		Filters   PossibleFilters `json:"possibleFilters"`
		ItemInfo  struct {
			Locations []TitleLocation `json:"locations"`
		} `json:"itemInfo"`
	} `json:"data"`
	Rapido struct {
		Services []TitleService `json:"serviceinfo"`
	} `json:"rapidoData"`
}

// TitleLocationRaw is the wire form of a location (organization kept).
// (Kept for documentation; Organization is parsed via UnmarshalJSON.)
type TitleLocationRaw struct {
	TitleLocation
	Organization string `json:"organization"`
}

// TitleServices fetches request options + item details for a record.
// Requires login (JWT): the service list depends on the user group.
func (c *Client) TitleServices(mms string) (*TitleServices, error) {
	if c.JWT == "" {
		return nil, fmt.Errorf("titleServices requires login: run `librarian auth login` first")
	}
	var raw titleServicesRaw
	q := url.Values{}
	if err := c.get("/primaws/rest/priv/ILSServices/titleServices/"+mms, q, &raw); err != nil {
		return nil, err
	}
	return rawToServices(&raw), nil
}

// TitleServicesDetail fetches the detail level for one service option
// (titleServices/<mms>/svcId/<serviceId>), as captured in the HAR file.
// Compared to TitleServices it additionally returns per-holding summaries
// (journal year/volume coverage), possibleFilters (year/volume selectors)
// and rapido resource-sharing options.
func (c *Client) TitleServicesDetail(mms, serviceID string) (*TitleServices, error) {
	if c.JWT == "" {
		return nil, fmt.Errorf("titleServices requires login: run `librarian auth login` first")
	}
	var raw titleServicesRaw
	q := url.Values{
		"hideResourceSharing": {"false"},
		"isRapido":            {"false"},
		"record-institution":  {Institution},
	}
	path := "/primaws/rest/priv/ILSServices/titleServices/" + mms + "/svcId/" + serviceID
	if err := c.get(path, q, &raw); err != nil {
		return nil, err
	}
	return rawToServices(&raw), nil
}

func rawToServices(raw *titleServicesRaw) *TitleServices {
	return &TitleServices{
		ServiceID:      raw.Data.ServiceID,
		Services:       raw.Data.Services,
		Locations:      raw.Data.ItemInfo.Locations,
		Filters:        raw.Data.Filters,
		RapidoServices: raw.Rapido.Services,
	}
}

// ---- account ----

// Loan is one active loan.
type Loan struct {
	Title   string `json:"title"`
	Author  string `json:"author"`
	DueDate string `json:"due-date"`
	Status  string `json:"status"`
	Barcode string `json:"barcode"`
}

// AccountLoans fetches active loans of the logged-in user.
func (c *Client) AccountLoans() ([]Loan, error) {
	var raw struct {
		Data struct {
			Loans struct {
				Loan []Loan `json:"loan"`
			} `json:"loans"`
		} `json:"data"`
	}
	if err := c.authenticatedGet("/priv/myaccount/loans", &raw); err != nil {
		return nil, err
	}
	return raw.Data.Loans.Loan, nil
}

// HoldRequest is one open request/hold/booking.
type HoldRequest struct {
	Title  string `json:"title"`
	Status string `json:"status"`
}

// AccountRequests fetches open holds, bookings and document-delivery requests.
func (c *Client) AccountRequests() (holds, bookings, photocopies []HoldRequest, err error) {
	var raw struct {
		Data struct {
			Holds struct {
				Hold []HoldRequest `json:"hold"`
			} `json:"holds"`
			Bookings struct {
				Booking []HoldRequest `json:"booking"`
			} `json:"bookings"`
			Photocopies struct {
				Photocopy []HoldRequest `json:"photocopy"`
			} `json:"photocopies"`
		} `json:"data"`
	}
	if err := c.authenticatedGet("/priv/myaccount/requests", &raw); err != nil {
		return nil, nil, nil, err
	}
	return raw.Data.Holds.Hold, raw.Data.Bookings.Booking, raw.Data.Photocopies.Photocopy, nil
}

func (c *Client) authenticatedGet(suffix string, out any) error {
	if c.JWT == "" {
		return fmt.Errorf("endpoint requires login: run `librarian auth login` first")
	}
	return c.get("/primaws/rest"+suffix, url.Values{}, out)
}
