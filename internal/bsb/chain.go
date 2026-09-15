package bsb

import (
	"fmt"
	"strings"
)

// ---- agent-facing request chain assembly (readonly preview + gated submit) ----

// RequestChain is the assembled browser-equivalent request path for one
// record: all readonly steps are executed during assembly (physicalServiceId,
// titleServices detail, holdings items, request form), the final POST is
// deferred to Submit and must be gated behind login + explicit --yes.
type RequestChain struct {
	Path    string // RequestPath(doc) classification
	Preview string // human-readable "what would happen"
	// Detail is the titleServices detail level (holdings, availability).
	Detail *TitleServices
	// Form is the raw request-form reply (services-arr) for the chosen item.
	Form map[string]any
	// Payload is the exact manipulated POST body that Submit would send.
	Payload map[string]any
	// submitLink + itemID are the POST target parts.
	submitLink string
	itemID     string
	ngrs       bool
	ngrsOffer  map[string]any
	note       string
}

// AssembleRequestChain assembles the full readonly chain for doc. pickup is the
// pickupLocation key prefix chosen by the user ("" = first form option);
// note is the requester note; lang selects the form language.
func AssembleRequestChain(client *Client, doc *Doc, pickup, note, lang string) (*RequestChain, error) {
	if client.JWT == "" {
		return nil, fmt.Errorf("request chain requires login: run `librarian auth login` first")
	}
	path := RequestPath(doc)
	if strings.HasPrefix(path, "ngrs:") {
		return ngrsChain(client, doc, note)
	}
	if !strings.HasPrefix(path, "ovp:") {
		return nil, fmt.Errorf("no physical request path for this record (%s); categories=%v modes=%v",
			doc.MMS(), doc.Delivery.DeliveryCategory, doc.Delivery.ServiceMode)
	}
	mms := doc.MMS()

	// 1. physicalServiceId (browser: getPhysicalService before detail).
	svcID, err := client.GetPhysicalService(doc.DocILSID(), doc.SourceRecordID(), doc.ResourceType(), doc.Delivery.RecordOwner)
	if err != nil || svcID == "" {
		return nil, fmt.Errorf("no physical service for %s (nothing requestable?): %v", mms, err)
	}
	// 2. titleServices detail at the physicalServiceId level.
	detail, err := client.TitleServicesDetail(mms, svcID)
	if err != nil {
		return nil, fmt.Errorf("titleServices detail: %w", err)
	}
	if len(detail.Locations) == 0 {
		return nil, fmt.Errorf("no holdings/locations for %s at service %s", mms, svcID)
	}
	loc := detail.Locations[0]

	// 3. holdings items POST → item id + per-item request links.
	itemsReply, err := client.HoldingsItems(svcID, 1, loc, nil)
	if err != nil {
		return nil, fmt.Errorf("holdings items: %w", err)
	}
	itemID, itemLink, barcode := firstRequestableItem(itemsReply)
	if itemID == "" {
		return nil, fmt.Errorf("no requestable item for %s (all copies non-requestable or on-site only?)", mms)
	}

	// 4. GET the itemServices form (readonly).
	form, err := client.RequestForm(itemLink)
	if err != nil {
		return nil, fmt.Errorf("request form: %w", err)
	}
	svcs := FormServices(form)
	if len(svcs) == 0 {
		return nil, fmt.Errorf("request form has no services-arr for item %s", itemID)
	}
	pickupOptions := pickupOptionKeys(svcs[0])
	chosen := pickup
	if chosen == "" && len(pickupOptions) > 0 {
		chosen = pickupOptions[0]
	} else if chosen != "" {
		// allow prefix match on the location id part.
		for _, o := range pickupOptions {
			if strings.HasPrefix(o, chosen) {
				chosen = o
				break
			}
		}
	}
	if chosen == "" {
		return nil, fmt.Errorf("request form offers no pickupLocation options")
	}

	// 5. manipulated payload (browser manipulatePostData).
	payload := PrepareSubmitPayload(map[string]any{
		"group_id":       mms,
		"pickupLocation": chosen,
	}, nil, mms)
	if note != "" {
		payload["patron_note_1"] = note
	}

	var b strings.Builder
	fmt.Fprintf(&b, "OVP loan chain for %s (%s):\n", doc.Title(), mms)
	fmt.Fprintf(&b, "  1. getPhysicalService → svcId %s\n", svcID)
	fmt.Fprintf(&b, "  2. holding %s (%s, %s) [%s]\n", loc.CallNumber, loc.MainLocation, loc.SubLocation, loc.AvailabilityStatement)
	fmt.Fprintf(&b, "  3. item %s (barcode %s) → %s\n", itemID, barcode, itemLink)
	fmt.Fprintf(&b, "  4. form type %v, pickup options: %d (chosen %s)\n", svcs[0]["type-name"], len(pickupOptions), shortKey(chosen))
	if note != "" {
		fmt.Fprintf(&b, "  note: %s\n", note)
	}
	fmt.Fprintf(&b, "  5. POST %s&lang=%s", itemLink, langOr(lang))
	return &RequestChain{
		Path: path, Preview: b.String(), Detail: detail,
		Form: form, Payload: payload,
		submitLink: itemLink, itemID: itemID,
	}, nil
}

// Submit executes the final state-changing POST. Callers must require login
// + explicit user confirmation (--yes) before calling.
func (rc *RequestChain) Submit(client *Client) (map[string]any, error) {
	if rc.ngrs {
		return client.SubmitBorrowingRequest(rc.Payload)
	}
	if client.JWT == "" {
		return nil, fmt.Errorf("submit requires login")
	}
	return client.SubmitRequest(rc.submitLink, "", rc.Payload)
}

// ngrsChain previews a resource-sharing request (offer fetch is readonly;
// the borrowingrequest POST stays deferred to Submit).
func ngrsChain(client *Client, doc *Doc, note string) (*RequestChain, error) {
	claims, err := DecodeJWT(client.JWT)
	userGroup := ""
	if err == nil {
		userGroup = claims.Group
	}
	params := BestOfferParams(doc, "", Institution, userGroup)
	offer, err := client.BestOffer("physical", params)
	if err != nil {
		return nil, fmt.Errorf("best offer: %w (resource sharing may be unavailable for %s)", err, doc.MMS())
	}
	payload := map[string]any{
		"title": doc.Title(),
		"mmsid": doc.MMS(),
		"offer": offerSummary(offer),
	}
	if note != "" {
		payload["requesterNote"] = note
	}
	var b strings.Builder
	fmt.Fprintf(&b, "NGRS resource-sharing chain for %s (%s):\n", doc.Title(), doc.MMS())
	fmt.Fprintf(&b, "  1. bestoffer/physical → %s\n", offerSummaryText(offer))
	fmt.Fprintf(&b, "  2. POST bestoffer/borrowingrequest (deferred until --yes)")
	return &RequestChain{
		Path: RequestPath(doc), Preview: b.String(),
		Payload: payload, ngrs: true, ngrsOffer: offer, note: note,
	}, nil
}

func langOr(lang string) string {
	if lang != "" {
		return lang
	}
	return "de"
}

func shortKey(k string) string {
	if i := strings.Index(k, "$$"); i >= 0 {
		return k[:i]
	}
	return k
}

// firstRequestableItem scans a holdings-items reply for the first item with a
// listofservices.service link, returning itemID, link and barcode.
func firstRequestableItem(reply map[string]any) (itemID, link, barcode string) {
	data, _ := reply["data"].(map[string]any)
	info, _ := data["itemInfo"].(map[string]any)
	locs, _ := info["locations"].([]any)
	for _, l := range locs {
		lm, _ := l.(map[string]any)
		items, _ := lm["items"].([]any)
		for _, it := range items {
			im, _ := it.(map[string]any)
			id, _ := im["itemid"].(string)
			bc, _ := im["itembarcode"].(string)
			los, _ := im["listofservices"].(map[string]any)
			svcs, _ := los["service"].([]any)
			for _, s := range svcs {
				sm, _ := s.(map[string]any)
				if sm["allowed"] == "Y" {
					if lk, _ := sm["link-to-service"].(string); lk != "" {
						return id, lk, bc
					}
				}
			}
		}
	}
	return "", "", ""
}

// pickupOptionKeys returns the pickupLocation option keys of one services-arr
// service (groups-list-map[].pickupLocation[].key).
func pickupOptionKeys(svc map[string]any) []string {
	var out []string
	groups, _ := svc["groups-list-map"].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		opts, _ := gm["pickupLocation"].([]any)
		for _, o := range opts {
			om, _ := o.(map[string]any)
			if k, _ := om["key"].(string); k != "" {
				out = append(out, k)
			}
		}
	}
	return out
}

func offerSummary(offer map[string]any) map[string]any {
	keys := []string{"supplyTime", "cost", "currency", "memberId", "policyId",
		"offerType", "loanPeriod", "lenderMemberName", "patronCost"}
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := offer[k]; ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return offer
	}
	return out
}

func offerSummaryText(offer map[string]any) string {
	var parts []string
	for _, k := range []string{"lenderMemberName", "supplyTime", "cost", "currency", "loanPeriod", "offerType"} {
		if v, ok := offer[k]; ok && v != nil && fmt.Sprintf("%v", v) != "" {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	if len(parts) == 0 {
		return "(offer reply without summary fields)"
	}
	return strings.Join(parts, " ")
}
