package bsb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ---- physical request chain (OVP / Alma-P), reconstructed from bundle.js ----
//
// Browser chain for a physical loan ("Ausleihe") in the BSB OPAC+ full view:
//
//  1. GET /primaws/rest/pub/pnxs/L/alma<mms> — record + delivery. The delivery
//     section carries holding[0].ilsApiId (record id for ILS calls),
//     pnx.control.sourcerecordid[0] (sourceRecordId), pnx.display.type[0]
//     (resource_type) and delivery.recordOwner.
//  2. GET /primaws/rest/pub/getPhysicalService/<ilsApiId>?vid=..&lang=..&
//     recordOwner=..&sourceRecordId=..&resource_type=..&isRapido=..
//     → {"physicalServiceId": "8594..."} (absent when nothing is requestable).
//  3. GET /primaws/rest/priv/ILSServices/titleServices/<mms>/svcId/<svcId>
//     (login JWT) → serviceinfo[] with per-type "link-to-service" URLs,
//     locations[] (hold-id, ils-api-id, holKey, availabilityStatement,
//     items with barcode/status/requestable).
//  4. POST /primaws/rest/priv/ILSServices/titleServices/<mms>[/svcId/<svcId>]
//     with the locations filter body (see HoldingsFilter) → item list
//     (barcode, item id, queue length) for the chosen holding.
//  5. GET <link-to-service> of the wanted request type → request form
//     ("services-arr": fields, pickup locations, defaults).
//  6. POST <link-to-service>[/item/<itemId>]&lang=.. with the filled form
//     (group_id, pickupLocation → pickUpLocation/pickupLibraryId,
//     INTERNAL fields stripped) → creates the hold/request.
//  7. GET /primaws/rest/priv/myaccount/requests to verify; POST
//     /primaws/rest/priv/myaccount/cancel_requests to cancel.
//
// Resource-sharing (Rapido/NGRS, Fernleihe) is a separate chain:
//
//  1. GET /primaws/rest/pub/ngrs/bestoffer/physical?mmsid=..&title=..&isbn=..&
//     ... (params from getParamsForPhysicalBestOffer: publicationDate, isbn,
//     issn, title, format, resourceType, edition, creator, volume, oclc,
//     issue, atitle, pickupInformation*, chosenPlaceRadio, formData,
//     instId, instCode, clientUserId) → best offer (lender, cost, supplyTime,
//     policyId, memberId, offerType, ...).
//  2. GET .../bestoffer/pickupLocation, .../bestoffer/email,
//     .../bestoffer/copyRightsStatement (readonly helpers).
//  3. POST .../bestoffer/borrowingrequest with the offer + form payload
//     (see BorrowingRequest) → creates the Rapido borrowing request.
//
// Only step 6 / NGRS step 3 change state; everything else is readonly.

// PhysicalServiceResponse is the reply of getPhysicalService.
type PhysicalServiceResponse struct {
	PhysicalServiceID string `json:"physicalServiceId"`
}

// GetPhysicalService resolves the svcId for a record (readonly).
// ilsID is holding[0].ilsApiId (fallback: MMS), sourceID is
// pnx.control.sourcerecordid[0], resourceType is pnx.display.type[0].
func (c *Client) GetPhysicalService(ilsID, sourceID, resourceType, recordOwner string) (string, error) {
	q := url.Values{
		"recordOwner":    {recordOwner},
		"sourceRecordId": {sourceID},
		"resource_type":  {resourceType},
		"isRapido":       {"null"},
	}
	var res PhysicalServiceResponse
	path := "/primaws/rest/pub/getPhysicalService/" + ilsID
	if err := c.get(path, q, &res); err != nil {
		return "", err
	}
	return res.PhysicalServiceID, nil
}

// DocILSID returns holding[0].ilsApiId, falling back to the MMS.
func (d *Doc) DocILSID() string {
	if len(d.Delivery.Holding) > 0 && d.Delivery.Holding[0].IlsAPIID != "" {
		return d.Delivery.Holding[0].IlsAPIID
	}
	return d.MMS()
}

// HoldingsFilter mirrors the locations POST body the UI builds in
// calculateRequestParams (bundle.js): filters selects the holding/page,
// locations carries the candidate locations.
//
// NOTE: the POST target is /priv/ILSServices/holdings/<svcId>
// (ilsServices.locations), NOT titleServices/<mms>/svcId/<svcId> — that one
// is GET-only. The filter body needs holKey + description for suprima, and
// locations entries carry both dash-case and camelCase location keys.
func (c *Client) HoldingsItems(svcID string, startPos int, loc TitleLocation, filters map[string]string) (map[string]any, error) {
	if c.JWT == "" {
		return nil, fmt.Errorf("holdings items require login: run `librarian auth login` first")
	}
	locBody := map[string]any{
		"main-location": loc.MainLocation, "sub-location": loc.SubLocation,
		"call-number": loc.CallNumber, "sub-location-code": loc.SubLocCode,
		"library-code": loc.LibraryCode, "ils-api-id": loc.IlsAPIID,
		"hold-id":      loc.HoldID,
		"holKey":       locHolKey(loc),
		"organization": loc.Organization(),
		"mainLocation": loc.MainLocation, "subLocationCode": loc.SubLocCode,
		"libraryCode": loc.LibraryCode, "ilsApiId": loc.IlsAPIID,
		"holdId": loc.HoldID, "callNumber": loc.CallNumber,
	}
	inst := loc.Organization()
	if inst == "" {
		inst = Institution
	}
	recID := loc.IlsAPIID
	body := map[string]any{
		"filters": map[string]any{
			"startPos": 1, "noItem": 11,
			"sublibrary": loc.MainLocation,
			"collection": "", "callnumber": "",
			"holid":   loc.HoldID,
			"sublibs": loc.MainLocation,
			"ilsRecordList": []map[string]string{
				{"institution": inst, "recordId": recID},
			},
			"vid":         c.View,
			"volume":      firstNonEmpty(filters["volume"], ""),
			"year":        firstNonEmpty(filters["year"], ""),
			"description": firstNonEmpty(filters["description"], ""),
			"holKey":      locHolKey(loc),
		},
		"locations":           []any{locBody},
		"hideResourceSharing": false,
	}
	_ = startPos
	path := "/primaws/rest/priv/ILSServices/holdings/" + svcID
	q := url.Values{}
	if inst != "" {
		q.Set("record-institution", inst)
	}
	var out map[string]any
	if err := c.post(path, q, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func locHolKey(loc TitleLocation) string {
	return fmt.Sprintf("HoldingResultKey [mid=%s, libraryId=112255960007356, locationCode=%s, callNumber=%s]",
		loc.HoldID, loc.SubLocCode, loc.CallNumber)
}

// LocationsForFilter reports the location identity used for fan-out.
func (l TitleLocation) LocationsForFilter() []string { return []string{l.HoldID} }

// Organization returns the owning institution of the location if known.
func (l TitleLocation) Organization() string { return l.org }

func firstNonEmpty(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// RequestForm fetches the request form for one service option (readonly GET
// of link-to-service). The reply carries "services-arr" (form fields,
// pickup locations, defaults) used to build the submit payload.
func (c *Client) RequestForm(link string) (map[string]any, error) {
	u := link
	if strings.HasPrefix(u, "/") {
		u = BaseURL + u
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if c.JWT != "" {
		req.Header.Set("Authorization", "Bearer "+c.JWT)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request form GET: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("request form GET failed with HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode request form: %w (body: %s)", err, truncate(string(raw), 300))
	}
	return out, nil
}

// FormServices extracts services-arr.services from a request form reply.
func FormServices(form map[string]any) []map[string]any {
	arr, _ := form["services-arr"].(map[string]any)
	svcs, _ := arr["services"].([]any)
	var out []map[string]any
	for _, s := range svcs {
		if m, ok := s.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// SubmitRequest POSTs a filled request form (STATE-CHANGING: creates a hold).
// link is the service's link-to-service; itemID appends "/item/<itemId>" for
// item-level requests; payload is the manipulated form data (group_id set,
// INTERNAL fields stripped, pickupLocation split into pickUpLocation +
// pickupLibraryId). Callers must gate this behind --dry-run default + --yes.
func (c *Client) SubmitRequest(link, itemID string, payload map[string]any) (map[string]any, error) {
	if c.JWT == "" {
		return nil, fmt.Errorf("submitting a request requires login: run `librarian auth login` first")
	}
	u := link
	if strings.HasPrefix(u, "/") {
		u = BaseURL + u
	}
	if itemID != "" {
		u += "/item/" + itemID
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	u += sep + "lang=" + url.QueryEscape(c.Lang)
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", u, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.JWT)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("submit request POST: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("submit request failed with HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode submit reply: %w (body: %s)", err, truncate(string(body), 300))
	}
	return out, nil
}

// PrepareSubmitPayload applies the browser's manipulatePostData rules to a
// form map: drop INTERNAL fields (caller passes their names), split
// "pickupLocation" ("<id>$$<lib>[###<addr>]") into pickUpLocation,
// pickupLibraryId (+userAddressId), ensure group_id defaults to ilsAPIID.
func PrepareSubmitPayload(form map[string]any, internalFields []string, ilsAPIID string) map[string]any {
	out := map[string]any{}
	for k, v := range form {
		out[k] = v
	}
	for _, f := range internalFields {
		delete(out, f)
	}
	if v, ok := out["pickupLocation"].(string); ok && strings.Contains(v, "$$") {
		id := v[:strings.Index(v, "$$")]
		rest := v[strings.Index(v, "$$")+2:]
		out["pickupLocation"] = id
		out["pickUpLocation"] = id
		out["pickupLibraryId"] = id
		if i := strings.Index(rest, "###"); i >= 0 {
			out["userAddressId"] = rest[i+3:]
		}
	}
	if _, ok := out["group_id"]; !ok {
		if g, ok := out["groupId"].(string); ok && g != "" {
			out["group_id"] = g
		} else if ilsAPIID != "" {
			out["group_id"] = ilsAPIID
		}
	}
	return out
}

// QueueCount POSTs the manipulated form to the calculatePlaceInQueue variant
// of the link (readonly): titleServices→calculatePlaceInQueue (or
// itemServices→calculatePlaceInQueue). Returns the raw reply.
func (c *Client) QueueCount(link string, payload map[string]any) (map[string]any, error) {
	ql := link
	if strings.Contains(ql, "titleServices") {
		ql = strings.Replace(ql, "titleServices", "calculatePlaceInQueue", 1)
	} else if strings.Contains(ql, "itemServices") {
		ql = strings.Replace(ql, "itemServices", "calculatePlaceInQueue", 1)
	} else {
		return nil, fmt.Errorf("no queue-count URL derivable from %q", link)
	}
	if strings.HasPrefix(ql, "/") {
		ql = BaseURL + ql
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", ql, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	if c.JWT != "" {
		req.Header.Set("Authorization", "Bearer "+c.JWT)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("queue count failed with HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode queue count: %w", err)
	}
	return out, nil
}

// ---- Rapido / NGRS resource sharing (Fernleihe) ----

// BestOfferParams mirrors getParamsForPhysicalBestOffer (bundle.js): the GET
// query for /pub/ngrs/bestoffer/physical (or /eBook). instId/instCode identify
// the BSB institution; clientUserId is the JWT userGroup.
func BestOfferParams(doc *Doc, instID, instCode, clientUserGroup string) url.Values {
	q := url.Values{
		"mmsid":            {doc.MMS()},
		"instId":           {instID},
		"instCode":         {instCode},
		"clientUserId":     {clientUserGroup},
		"title":            {doc.Title()},
		"format":           {firstOf(doc.Pnx.Addata.Format)},
		"resourceType":     {doc.ResourceType()},
		"creator":          {firstOf(doc.Pnx.Display.Creator)},
		"isbn":             {firstOf(doc.Pnx.Addata.ISBN)},
		"issn":             {firstOf(doc.Pnx.Addata.ISSN)},
		"publicationDate":  {firstOf(doc.Pnx.Display.CreationDate)},
		"edition":          {""},
		"volume":           {""},
		"oclc":             {""},
		"issue":            {""},
		"atitle":           {""},
		"formData":         {""},
		"chosenPlaceRadio": {""},
	}
	if len(doc.Pnx.Addata.ISBN) > 0 {
		q.Set("isbn", doc.Pnx.Addata.ISBN[0])
	}
	return q
}

func firstOf(in []string) string {
	if len(in) > 0 {
		return in[0]
	}
	return ""
}

// BestOffer fetches the Rapido best offer (readonly GET). kind is
// "physical", "digital" or "eBook".
func (c *Client) BestOffer(kind string, params url.Values) (map[string]any, error) {
	if kind == "" {
		kind = "physical"
	}
	var out map[string]any
	if err := c.get("/primaws/rest/pub/ngrs/bestoffer/"+kind, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RequestPath classifies how the browser would render the request path for a
// record, so agents know which chain to follow. It mirrors the bundle.js
// decision: serviceMode "ovp" → local Alma request chain (titleServices /
// itemServices, see HoldingsItems/RequestForm/SubmitRequest); Rapido/NGRS
// best-offer chain for resource-sharing; "Viewit"/electronic otherwise.
func RequestPath(doc *Doc) string {
	for _, m := range doc.Delivery.ServiceMode {
		if m == "ovp" {
			return "ovp: local Alma request — getPhysicalService → titleServices → holdings items → itemServices form → POST submit"
		}
	}
	for _, c := range doc.Delivery.DeliveryCategory {
		if c == "Alma-P" {
			return "ovp: local Alma request — getPhysicalService → titleServices → holdings items → itemServices form → POST submit"
		}
		if c == "Alma-E" || c == "Alma-D" {
			return "electronic: edelivery/viewit services, no physical loan"
		}
		if c == "Remote Search Resource" {
			return "ngrs: resource sharing — bestoffer/physical → borrowingrequest"
		}
	}
	return "unknown: check titleServices serviceinfo link-to-service"
}

// PickupLocations fetches the NGRS pickup-location map (readonly).
func (c *Client) PickupLocations() (map[string]any, error) {
	var out map[string]any
	if err := c.get("/primaws/rest/pub/ngrs/bestoffer/pickupLocation", url.Values{}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// BorrowingRequest is the POST payload for /pub/ngrs/bestoffer/borrowingrequest
// (see createBorrowingRequest in bundle.js). Only the fields the CLI sets are
// modeled; offer-derived fields come from the best-offer reply.
type BorrowingRequest struct {
	Title         string `json:"title,omitempty"`
	ISBN          string `json:"isbn,omitempty"`
	ISSN          string `json:"issn,omitempty"`
	RequesterNote string `json:"requesterNote,omitempty"`
	DueDate       string `json:"dueDate,omitempty"`
	Volume        string `json:"volume,omitempty"`
	// Offer fields (copied from the best offer):
	SupplyTime string `json:"supplyTime,omitempty"`
	Cost       any    `json:"cost,omitempty"`
	Currency   string `json:"currency,omitempty"`
	MemberID   string `json:"memberId,omitempty"`
	PolicyID   string `json:"policyId,omitempty"`
	OfferType  string `json:"offerType,omitempty"`
	LoanPeriod string `json:"loanPeriod,omitempty"`
	Format     string `json:"format,omitempty"`
	// Pickup:
	PickUpLocation string `json:"pickUpLocation,omitempty"`
	// Copyright / service flags:
	CopyrightApproved string `json:"copyrightapproved,omitempty"`
}

// SubmitBorrowingRequest POSTs a Rapido borrowing request (STATE-CHANGING).
// Gate behind --dry-run default + --yes like SubmitRequest.
func (c *Client) SubmitBorrowingRequest(payload any) (map[string]any, error) {
	if c.JWT == "" {
		return nil, fmt.Errorf("submitting a borrowing request requires login: run `librarian auth login` first")
	}
	var out map[string]any
	if err := c.post("/primaws/rest/pub/ngrs/bestoffer/borrowingrequest", url.Values{}, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CancelRequests cancels open requests (STATE-CHANGING). ids are the request
// identifiers as returned by myaccount/requests. Gate behind --yes.
func (c *Client) CancelRequests(ids []string) (map[string]any, error) {
	if c.JWT == "" {
		return nil, fmt.Errorf("cancelling requests requires login: run `librarian auth login` first")
	}
	var out map[string]any
	if err := c.post("/primaws/rest/priv/myaccount/cancel_requests", url.Values{}, map[string]any{"requestIds": ids}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) post(path string, query url.Values, body any, out any) error {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(raw)
	}
	u := BaseURL + path
	if query == nil {
		query = url.Values{}
	}
	if _, ok := query["vid"]; !ok {
		query.Set("vid", c.View)
	}
	if _, ok := query["lang"]; !ok {
		query.Set("lang", c.Lang)
	}
	if enc := query.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequest("POST", u, r)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	if c.JWT != "" {
		req.Header.Set("Authorization", "Bearer "+c.JWT)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("request POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("request %s failed with HTTP %d: %s", path, resp.StatusCode, truncate(string(raw), 300))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s: %w (body: %s)", path, err, truncate(string(raw), 300))
	}
	return nil
}
