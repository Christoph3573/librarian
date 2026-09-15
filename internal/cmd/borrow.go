package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Christoph3573/librarian/internal/bsb"
	"github.com/Christoph3573/librarian/internal/session"
)

func newBorrowCmd() *cobra.Command {
	var mms, outDir string
	var dryRun bool
	var maxPages int
	var detail bool
	var offerKind, pickup, note string
	var yes bool
	var cancel string
	var openBrowser bool
	cmd := &cobra.Command{
		Use:   "borrow",
		Short: "Borrow media: download free scans / place physical + resource-sharing requests",
		Long: `Borrow the best legal access path for a record (by MMS-ID).

The entitlement router decides what happens — the CLI never treats an
arbitrary PNX link as a PDF URL:

  download (free full text, anonymous OK):
    MDZ scans (mdz-nbn-resolving.de → IIIF page images into --out-dir),
    open repositories and direct PDFs. Free downloads need no --yes.

  electronic-licensed (Alma-E/Viewit, login improves the check):
    the edelivery endpoint is the authoritative source for hasAccess.
    For Alma-E records check 'auth status --json' FIRST: if anonymous
    and the route is request-physical, run 'auth login' and retry —
    the anonymous check underestimates access. Depending on the
    winning offer 'borrow' either
      - open-browser: licensed for you → prints the resolver URL
        (publisher SSO happens in the browser; with --open the browser
        is launched directly),
      - search-in-portal: portal/collection page, not the title → prints
        the portal URL to search inside,
      - request-physical: licensed but no access → falls through to the
        physical / resource-sharing chain below.

  physical media / resource sharing (requires login):
    the browser chain is fully modeled (see 'request_path' in
    inspect --json):

  ovp (local Alma loan, serviceMode=ovp / category Alma-P):
    1. getPhysicalService → physical_service_id (svcId)
    2. titleServices detail (hold-id, ils-api-id, holKey, availability)
    3. holdings items POST → item id + per-item request links
       (e.g. .../itemServices/<mms>/item/<itemId>/<svcId>/AlmaItemRequest)
    4. GET the itemServices link → request form (pickupLocation options)
    5. POST the filled form → hold created; verify with 'auth status'

  ngrs (resource sharing / Fernleihe, category Remote Search Resource):
    1. GET bestoffer/physical (readonly offer: lender, cost, supplyTime)
    2. POST bestoffer/borrowingrequest (creates the request)

SAFETY: --dry-run is the default behavior for anything that is not a free
download: without --yes, 'borrow' only shows the chain (offer/form/payload)
and never POSTs a request. A real request needs BOTH login AND --yes:

  borrow --mms <id> --dry-run --json     # preview only (default, safe)
  borrow --mms <id> --yes --json         # REAL request (login required)
  borrow --mms <id> --offer physical     # readonly best-offer preview (ngrs)
  borrow --cancel <request-id> --yes     # cancel an open request

  AGENT HINT: run 'librarian borrow --help' first. Get the MMS-ID from
  'librarian research --json' and check 'librarian inspect --mms <id> --json'
  (request_path, physical_service_id) before borrowing. Prefer --json.
  For Alma-E: if not logged in ('logged_in': false in inspect) and the
  route is request-physical, 'borrow' stops and demands login first —
  retry after 'auth login' before ordering anything physically.

Examples:
  # preview what would happen (no download, no request):
  librarian borrow --mms 991032822189707356 --dry-run --json

  # physical loan chain preview (login required for steps 2-4):
  librarian borrow --mms 991144111495607356 --detail --json

  # real physical request (login + explicit confirmation):
  librarian borrow --mms 991144111495607356 --yes --pickup 13725028710007356

  # download free MDZ scan (first 5 pages to check):
  librarian borrow --mms 991032822189707356 --out-dir ./faust --max-pages 5

  # licensed e-book (entitled): show the resolver URL, or open it:
  librarian borrow --mms 991146664888507356 --json
  librarian borrow --mms 991146664888507356 --open`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// --- cancel path (needs no MMS lookup) ---
			if cancel != "" {
				client := bsb.NewClient(flagView, flagLang, "")
				sess, err := session.Load()
				if err != nil || sess.Expired() {
					return fmt.Errorf("cancelling requires login: run `librarian auth login` first")
				}
				client.JWT = sess.JWT
				if !yes {
					if flagJSON {
						return printJSON(map[string]any{"mode": "cancel", "dry_run": true,
							"message": fmt.Sprintf("would cancel request %s (re-run with --yes to confirm)", cancel)})
					}
					fmt.Printf("would cancel request %s (re-run with --yes to confirm)\n", cancel)
					return nil
				}
				reply, err := client.CancelRequests([]string{cancel})
				if err != nil {
					return err
				}
				if flagJSON {
					return printJSON(map[string]any{"mode": "cancel", "dry_run": false, "reply": reply})
				}
				fmt.Printf("cancel reply: %v\n", reply)
				return nil
			}
			if mms == "" {
				if len(args) > 0 {
					mms = args[0]
				} else {
					return fmt.Errorf("an MMS-ID is required: librarian borrow --mms <MMS-ID>")
				}
			}
			client := bsb.NewClient(flagView, flagLang, "")
			loggedIn := false
			if sess, err := session.Load(); err == nil && !sess.Expired() {
				client.JWT = sess.JWT
				loggedIn = true
			}
			doc, err := client.SearchByMMS(mms, true)
			if err != nil {
				return err
			}

			// --- entitlement router: classify PNX links + edelivery access ---
			offers, edErr := bsb.ResolveElectronic(client, doc)
			route := bsb.BestElectronicRoute(offers)

			res := bsb.BorrowResult{MMS: doc.MMS(), Title: doc.Title(), DryRun: dryRun || !yes,
				Electronic: offers, Route: route, ElectronicError: edErr, LoggedIn: loggedIn}
			if route != nil {
				res.Action = route.Action
			}
			if bsb.LoginHintNeeded(offers, loggedIn) {
				res.LoginHint = bsb.LoginHint
			}

			// --- readonly best-offer probe (ngrs) ---
			if offerKind != "" {
				params := bsb.BestOfferParams(doc, "", "49BVB_BSB", "")
				offer, err := client.BestOffer(offerKind, params)
				if err != nil {
					return fmt.Errorf("best offer (%s): %w", offerKind, err)
				}
				if flagJSON {
					return printJSON(map[string]any{"mms": doc.MMS(), "mode": "offer", "kind": offerKind, "dry_run": true, "offer": offer})
				}
				fmt.Printf("best offer (%s): %v\n", offerKind, offer)
				return nil
			}

			// --- routed electronic actions (no --yes needed: nothing state-changing) ---
			if route != nil {
				switch route.Action {
				case bsb.ActionDownload:
					// handled below (MDZ / direct download)
				case bsb.ActionOpenBrowser:
					if err := emitOpenBrowser(doc, res, route, edErr, openBrowser); err != nil {
						return err
					}
					return nil
				case bsb.ActionSearchInPortal:
					if err := emitSearchInPortal(doc, res, route, edErr); err != nil {
						return err
					}
					return nil
				case bsb.ActionRequestPhysical:
					// Licensed, but no access for this user. Anonymous checks
					// systematically underestimate access, so stop here and
					// demand login instead of falling into the (login-gated)
					// physical chain with a misleading "requires login" error.
					if err := emitLoginRequired(doc, res, route); err != nil {
						return err
					}
					return nil
				}
			}

			// --- physical / resource-sharing request chain (login required) ---
			// Without --yes this is strictly readonly (preview = --dry-run).
			// Reachable for records without online offers (route == nil);
			// licensed-but-denied records stopped above at emitLoginRequired
			// (anonymous) or fall here only when already logged in.
			// Download routes skip the chain entirely (free downloads need no
			// login); browser/portal routes already returned above.
			if route == nil || (route.Action == bsb.ActionRequestPhysical && loggedIn) {
				// Logged-in licensed-but-denied: the physical fallback is
				// genuine — say so before the chain preview.
				if route != nil {
					printDeniedElectronic(doc, res, route)
				}
				chain, err := bsb.AssembleRequestChain(client, doc, pickup, note, flagLang)
				if err != nil {
					if flagJSON {
						return printJSON(newRequestErrorJSON(doc, res, chainPathOf(doc), err))
					}
					return err
				}
				if !yes {
					res.Mode = "request"
					res.DryRun = true
					res.Message = chain.Preview
					res.Detail = chain.Detail
					if flagJSON {
						return printJSON(res.WithRequestPreview(chain))
					}
					fmt.Println(chain.Preview)
					fmt.Println("\nRe-run with --yes (+ login) to place this request for real.")
					return nil
				}
				// REAL submit: login + --yes gate already checked (RequestChain
				// errors when not logged in).
				reply, err := chain.Submit(client)
				if err != nil {
					return err
				}
				if flagJSON {
					return printJSON(map[string]any{"mms": doc.MMS(), "mode": "request", "dry_run": false, "reply": reply})
				}
				fmt.Printf("request placed: %v\n", reply)
				fmt.Println("Verify with: librarian auth status")
				return nil
			}

			// --- real download: only the router's open links download ---
			var mdz, free []bsb.DeliveryLink
			if route != nil && route.Action == bsb.ActionDownload {
				// The winning offer decides: best-effort download of every open
				// link (MDZ via IIIF, the rest direct). Portal/aggregator links
				// never reach this branch — ClassifyLink keeps them out.
				for _, l := range doc.OnlineLinks() {
					if bsb.ClassifyLink(l) != bsb.KindOpen {
						continue
					}
					if bsb.IsMDZLink(l.LinkURL) {
						mdz = append(mdz, l)
					} else {
						free = append(free, l)
					}
				}
			}
			if len(mdz) > 0 {
				objID, err := bsb.ResolveMDZ(client.HTTP, mdz[0].LinkURL)
				if err != nil {
					return fmt.Errorf("resolve MDZ link: %w", err)
				}
				dir := outDir
				if dir == "" {
					dir = filepath.Join(".", objID)
				}
				fmt.Fprintf(os.Stderr, "Downloading MDZ object %s …\n", objID)
				files, total, err := bsb.DownloadMDZ(client.HTTP, objID, dir, maxPages, func(done, total int) {
					fmt.Fprintf(os.Stderr, "\r  page %d/%d", done, total)
				})
				fmt.Fprintln(os.Stderr)
				if err != nil {
					return err
				}
				res.Mode = "download"
				res.Files = files
				res.Message = fmt.Sprintf("downloaded %d/%d pages of %s to %s", len(files), total, objID, dir)
				if flagJSON {
					return printJSON(res)
				}
				fmt.Println(res.Message)
				return nil
			}
			if len(free) > 0 {
				dir := outDir
				if dir == "" {
					dir = filepath.Join(".", doc.MMS())
				}
				var files []string
				for _, l := range free {
					f, err := bsb.DownloadDirect(client.HTTP, l.LinkURL, dir)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warning: %s: %v\n", l.LinkURL, err)
						continue
					}
					files = append(files, f)
				}
				if len(files) == 0 {
					return fmt.Errorf("all %d download(s) failed", len(free))
				}
				res.Mode = "download"
				res.Files = files
				res.Message = fmt.Sprintf("downloaded %d file(s) to %s", len(files), dir)
				if flagJSON {
					return printJSON(res)
				}
				fmt.Println(res.Message)
				for _, f := range files {
					fmt.Printf("  %s\n", f)
				}
				return nil
			}

			// --- no digital copy fallback (chain path above handles requests) ---
			res.Mode = "request-info"
			if route != nil && len(offers) == 0 {
				res.Message = "no online links and no physical request path"
			} else {
				res.Message = "no free digital copy available; physical request required"
			}
			if flagJSON {
				if detail {
					res.Detail = requestDetail(doc, client, loggedIn)
				}
				return printJSON(res)
			}
			fmt.Println(res.Message)
			printRequestHint(doc, client, loggedIn, detail)
			return nil
		},
	}
	cmd.Flags().StringVar(&mms, "mms", "", "Alma MMS-ID of the record (from research)")
	cmd.Flags().StringVar(&outDir, "out-dir", "", "download directory (default ./<object-id>)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only show what would happen, download/request nothing (default for requests without --yes)")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm a REAL state-changing request (login required); without --yes everything is preview-only")
	cmd.Flags().StringVar(&offerKind, "offer", "", "readonly NGRS best-offer probe: physical|digital|eBook (no request placed)")
	cmd.Flags().StringVar(&pickup, "pickup", "", "pickup location id for the request form (from the form's pickupLocation options; default: first option)")
	cmd.Flags().StringVar(&note, "note", "", "requester note sent with the request")
	cmd.Flags().StringVar(&cancel, "cancel", "", "cancel an open request by id (needs --yes to confirm; no --mms needed)")
	cmd.Flags().IntVar(&maxPages, "max-pages", 0, "max IIIF pages to download (0 = all; useful for checks)")
	cmd.Flags().BoolVar(&detail, "detail", false, "resolve per-service holdings detail (summaries, copy statements) for physical requests (login required)")
	cmd.Flags().BoolVar(&openBrowser, "open", false, "open the licensed resolver URL in the browser (for open-browser routes; default prints the URL only)")
	return cmd
}

// emitOpenBrowser reports a licensed, entitled offer. The CLI never speaks
// publisher SSO — the resolver URL is printed (JSON: electronic_route) and
// only launched with explicit --open.
func emitOpenBrowser(doc *bsb.Doc, res bsb.BorrowResult, route *bsb.ElectronicOffer, edErr string, launch bool) error {
	res.Mode = "open-browser"
	res.DryRun = !launch
	target := route.ResolverURL
	if target == "" {
		target = route.DirectURL
	}
	res.Message = fmt.Sprintf("licensed access via %s — publisher SSO happens in the browser: %s", route.Platform, target)
	if flagJSON {
		return printJSON(res)
	}
	fmt.Println(res.Message)
	if edErr != "" {
		fmt.Fprintf(os.Stderr, "(entitlement check incomplete: %s)\n", edErr)
	}
	fmt.Printf("Record: %s (MMS %s)\n", doc.Title(), doc.MMS())
	if launch {
		if err := openURL(target); err != nil {
			return err
		}
		fmt.Println("Opened in browser.")
		return nil
	}
	fmt.Println("Re-run with --open to launch the browser, or open the URL above manually.")
	return nil
}

// emitSearchInPortal reports a package-portal offer: the PNX link is a
// collection/database page, not the title. No download is attempted.
func emitSearchInPortal(doc *bsb.Doc, res bsb.BorrowResult, route *bsb.ElectronicOffer, edErr string) error {
	res.Mode = "search-in-portal"
	res.DryRun = true
	res.Message = fmt.Sprintf("no direct title link — search %q inside %s: %s", doc.Title(), route.Platform, route.DirectURL)
	if flagJSON {
		return printJSON(res)
	}
	fmt.Println(res.Message)
	if edErr != "" {
		fmt.Fprintf(os.Stderr, "(entitlement check incomplete: %s)\n", edErr)
	}
	fmt.Printf("Record: %s (MMS %s)\n", doc.Title(), doc.MMS())
	if route.Reason != "" {
		fmt.Printf("(%s)\n", route.Reason)
	}
	return nil
}

// emitLoginRequired stops anonymous licensed-but-denied borrows with an
// actionable login demand (JSON: login_hint + entitlement offers) instead
// of falling into the physical chain. Logged-in denied records never reach
// here — for them the physical fallback below is genuine.
func emitLoginRequired(doc *bsb.Doc, res bsb.BorrowResult, route *bsb.ElectronicOffer) error {
	res.Mode = "login-required"
	res.DryRun = true
	res.LoginHint = bsb.LoginHint
	res.Message = fmt.Sprintf("licensed electronic access denied for anonymous check (package: %s) — log in and retry before ordering physically: `librarian auth login`, then `librarian borrow --mms %s`", route.Platform, doc.MMS())
	if flagJSON {
		return printJSON(res)
	}
	fmt.Println(res.Message)
	fmt.Printf("Record: %s (MMS %s)\n", doc.Title(), doc.MMS())
	fmt.Printf("Hint: %s\n", bsb.LoginHint)
	return nil
}

// printDeniedElectronic informs about licensed-but-denied offers for
// logged-in users, right before the genuine physical-chain fallback.
func printDeniedElectronic(doc *bsb.Doc, res bsb.BorrowResult, route *bsb.ElectronicOffer) {
	if flagJSON {
		return // the JSON envelope carries action + electronic offers
	}
	fmt.Printf("Licensed electronic access denied even when logged in (package: %s).\n", route.Platform)
	fmt.Println("Falling back to the physical / resource-sharing chain:")
}

// newRequestErrorJSON builds the machine-readable error envelope for a
// failed request-chain assembly: route action, login state, entitlement
// offers and the request path stay visible so agents can react (e.g. login,
// then retry).
func newRequestErrorJSON(doc *bsb.Doc, res bsb.BorrowResult, requestPath string, err error) map[string]any {
	res.Mode = "request"
	res.DryRun = true
	res.RequestPath = requestPath
	out := map[string]any{
		"mms": doc.MMS(), "title": doc.Title(),
		"mode": res.Mode, "dry_run": true,
		"action": res.Action, "logged_in": res.LoggedIn,
		"electronic":       res.Electronic,
		"electronic_route": res.Route, "request_path": requestPath,
		"error": err.Error(),
	}
	if res.LoginHint != "" {
		out["login_hint"] = res.LoginHint
	}
	if res.ElectronicError != "" {
		out["electronic_error"] = res.ElectronicError
	}
	return out
}

// chainPathOf classifies the record without running the (login-gated) chain.
func chainPathOf(doc *bsb.Doc) string {
	return bsb.RequestPath(doc)
}

func printRequestHint(doc *bsb.Doc, client *bsb.Client, loggedIn, detail bool) {
	fmt.Printf("\nRecord: %s (MMS %s)\n", doc.Title(), doc.MMS())
	for _, h := range doc.Delivery.Holding {
		fmt.Printf("  copy: %s — %s, shelfmark %s [%s]\n",
			h.MainLocation, h.SubLocation, h.CallNumber, h.AvailabilityStatus)
	}
	if !loggedIn {
		fmt.Println("\nFor request options: `librarian auth login`, then `librarian inspect --mms <id>`.")
		fmt.Println("Place the loan/reading-room order in your OPAC+ account (Mein Konto > Bestellen).")
		return
	}
	svc, err := client.TitleServices(doc.MMS())
	if err != nil {
		fmt.Printf("\nRequest options unavailable: %v\n", err)
		return
	}
	fmt.Println("\nRequest options (from titleServices):")
	for _, s := range svc.Services {
		fmt.Printf("  - %s [%s] allowed=%s\n", s.Type, s.ServiceType, s.Allowed)
	}
	if detail {
		d, err := client.TitleServicesDetail(doc.MMS(), svc.ServiceID)
		if err != nil {
			fmt.Printf("\nHoldings detail unavailable: %v\n", err)
		} else {
			printDetail(d)
		}
	}
	fmt.Println("Place the request in your OPAC+ account (Mein Konto > Bestellen) or at the desk.")
}

// requestDetail resolves titleServices + svcId detail for JSON output.
func requestDetail(doc *bsb.Doc, client *bsb.Client, loggedIn bool) *bsb.TitleServices {
	if !loggedIn {
		return nil
	}
	svc, err := client.TitleServices(doc.MMS())
	if err != nil {
		return nil
	}
	d, err := client.TitleServicesDetail(doc.MMS(), svc.ServiceID)
	if err != nil {
		return svc
	}
	return d
}
