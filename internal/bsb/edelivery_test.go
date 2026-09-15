package bsb

import (
	"strings"
	"testing"
)

func link(url string) DeliveryLink {
	return DeliveryLink{LinkType: "linktorsrc", LinkURL: url, DisplayLabel: "Volltext Deutschlandweit zugänglich"}
}

func TestClassifyLink(t *testing.T) {
	cases := []struct {
		url  string
		want ElectronicKind
	}{
		{"http://mdz-nbn-resolving.de/urn:nbn:de:bvb:12-bsb00000001-1", KindOpen},
		{"https://www.digitale-sammlungen.de/view/bsb00000001", KindOpen},
		{"https://arxiv.org/pdf/2401.00001", KindOpen},
		{"https://example.com/files/chapter.pdf?download=1", KindOpen},
		{"http://mdz-nbn-resolving.de/urn:nbn:de:bvb:12-bsb00000001-1", KindOpen}, // label-independent
		{"/view/action/uresolver.do?operation=resolveService&package_service_id=1", KindResolver},
		{"https://doi.org/10.1000/xyz", KindResolver},
		{"https://dbis.ur.de/resources/10248", KindPackagePortal},
		{"http://link.springer.com/search?package=11650&facet-content-type=%22Book%22", KindPackagePortal},
		{"https://search.ebscohost.com/login.aspx?authtype=ip,uid&profile=ehost", KindPackagePortal},
		{"http://www.sciencedirect.com/science/books/sub/mathematics", KindPackagePortal},
		{"https://platform.almanhal.com/", KindPackagePortal},
		{"https://www.degruyterbrill.com/serial/TUSC-B/html", KindPackagePortal},
		{"https://ebookcentral.proquest.com/lib/bsb/detail.action?docID=123", KindAggregatorTitle},
		{"https://link.springer.com/book/10.1007/978-3-540-00000-0", KindAggregatorTitle},
		{"https://example.com/some/title/page", KindUnknown},
		{"", KindUnknown},
	}
	for _, tc := range cases {
		if got := ClassifyLink(link(tc.url)); got != tc.want {
			t.Errorf("ClassifyLink(%q) = %s, want %s", tc.url, got, tc.want)
		}
	}
}

func TestClassifyIgnoresLabel(t *testing.T) {
	// A portal page labeled "Volltext" must not become KindOpen.
	l := DeliveryLink{LinkType: "linktorsrc", LinkURL: "https://dbis.ur.de/resources/5", DisplayLabel: "Volltext"}
	if got := ClassifyLink(l); got != KindPackagePortal {
		t.Fatalf("portal labeled Volltext classified as %s", got)
	}
}

func TestBestElectronicRoute(t *testing.T) {
	yes := true
	offers := []ElectronicOffer{
		{Platform: "l", Kind: string(KindLicensedService), HasAccess: &yes, Action: ActionOpenBrowser},
		{Platform: "m", Kind: string(KindOpen), Action: ActionDownload},
	}
	if got := BestElectronicRoute(offers); got == nil || got.Action != ActionDownload {
		t.Fatalf("expected download to win, got %+v", got)
	}

	no := false
	denied := []ElectronicOffer{
		{Platform: "dbis", Kind: string(KindPackagePortal), Action: ActionSearchInPortal},
		{Platform: "Ebook Central", Kind: string(KindLicensedService), HasAccess: &no, Action: ActionRequestPhysical},
	}
	if got := BestElectronicRoute(denied); got == nil || got.Action != ActionSearchInPortal {
		t.Fatalf("expected portal hint before physical fallback, got %+v", got)
	}

	// Entitled licensed access must survive being listed AFTER a portal or
	// an unclassified browser link — order in the offer list is not ranking.
	unentitledFirst := []ElectronicOffer{
		{Platform: "dbis", Kind: string(KindPackagePortal), Action: ActionSearchInPortal},
		{Platform: "l", Kind: string(KindLicensedService), HasAccess: &yes, Action: ActionOpenBrowser},
	}
	if got := BestElectronicRoute(unentitledFirst); got == nil || got.Platform != "l" {
		t.Fatalf("expected entitled offer to win over portal, got %+v", got)
	}

	onlyDenied := []ElectronicOffer{
		{Platform: "Ebook Central", Kind: string(KindLicensedService), HasAccess: &no, Action: ActionRequestPhysical},
	}
	if got := BestElectronicRoute(onlyDenied); got == nil || got.Action != ActionRequestPhysical {
		t.Fatalf("expected physical fallback, got %+v", got)
	}

	if got := BestElectronicRoute(nil); got != nil {
		t.Fatalf("expected nil for no offers, got %+v", got)
	}
}

func TestElectronicAccessSkipsAddLinks(t *testing.T) {
	doc := &Doc{}
	doc.Delivery.Link = []DeliveryLink{
		{LinkType: "addlink", LinkURL: "https://opacplus.bsb-muenchen.de/title/1?format=marc", DisplayLabel: "▪ Datensatz (MARC21-XML)"},
		{LinkType: "linktorsrc", LinkURL: "https://dbis.ur.de/resources/9", DisplayLabel: "Volltext"},
	}
	got := ElectronicAccess(doc, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 offer (addlink skipped), got %d", len(got))
	}
	if got[0].Kind != string(KindPackagePortal) {
		t.Fatalf("expected package-portal, got %s", got[0].Kind)
	}
}

func TestElectronicAccessLicensedOffer(t *testing.T) {
	doc := &Doc{}
	ed := &EdeliveryResponse{ElectronicServices: []EdeliveryService{
		{PackageName: "Ebook Central", ServiceURL: "/view/action/uresolver.do?x=1", HasAccess: false},
	}}
	got := ElectronicAccess(doc, ed)
	if len(got) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(got))
	}
	o := got[0]
	if o.Action != ActionRequestPhysical {
		t.Fatalf("denied offer must route to request-physical, got %s", o.Action)
	}
	if o.ResolverURL == "" || !strings.HasPrefix(o.ResolverURL, BaseURL) {
		t.Fatalf("resolver URL must be absolute, got %q", o.ResolverURL)
	}
	if o.HasAccess == nil || *o.HasAccess {
		t.Fatalf("hasAccess must be present and false")
	}
}

func TestLoginHintNeeded(t *testing.T) {
	no := false
	denied := []ElectronicOffer{
		{Platform: "Ebook Central", Kind: string(KindLicensedService), HasAccess: &no, Action: ActionRequestPhysical},
	}
	if !LoginHintNeeded(denied, false) {
		t.Fatalf("anonymous denied licensed offer must need the login hint")
	}
	if LoginHintNeeded(denied, true) {
		t.Fatalf("logged-in check must not need the login hint")
	}
	portalOnly := []ElectronicOffer{
		{Platform: "dbis", Kind: string(KindPackagePortal), Action: ActionSearchInPortal},
	}
	if LoginHintNeeded(portalOnly, false) {
		t.Fatalf("portal-only offers must not need the login hint")
	}
	openOnly := []ElectronicOffer{
		{Platform: "mdz", Kind: string(KindOpen), Action: ActionDownload},
	}
	if LoginHintNeeded(openOnly, false) {
		t.Fatalf("open-only offers must not need the login hint")
	}
	// Licensed offer without entitlement flag (PNX-only browser link) is
	// not a denied check — no hint.
	unflagged := []ElectronicOffer{
		{Platform: "x", Kind: string(KindLicensedService), Action: ActionOpenBrowser},
	}
	if LoginHintNeeded(unflagged, false) {
		t.Fatalf("offer without hasAccess flag must not need the login hint")
	}
	if LoginHint == "" {
		t.Fatalf("LoginHint must be a non-empty retry instruction")
	}
}
