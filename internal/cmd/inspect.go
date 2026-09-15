package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Christoph3573/librarian/internal/bsb"
	"github.com/Christoph3573/librarian/internal/session"
)

func newInspectCmd() *cobra.Command {
	var mms string
	var detail bool
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Show formats + availability of one record",
		Long: `Show the format(s) of a record and how each is available.

For one MMS-ID this shows:
  - bibliographic data (title, creators, year, publisher, ISBN, subjects)
  - media formats: physical holdings (library, location, call number, status)
    and digital/online links (free full text, TOC, e-book portals)
  - request options from titleServices when logged in (order/loan/document
    delivery, allowed Y/N) — anonymous users get a login hint instead
  - with --detail: the per-service detail level (titleServices/<mms>/svcId/…):
    holdings summaries (journal year/volume coverage, notes), copy statements
    ("1 Exemplar, 1 verfügbar"), possible year/volume filters and
    resource-sharing (rapido) options

AGENT HINT: run 'librarian inspect --help' first. Get the MMS-ID from
'librarian research --json'. Prefer --json. Use --detail for journals and
multi-volume works where the plain holdings list is not enough.

Examples:
  librarian inspect --mms 991032822189707356
  librarian inspect --mms 991032822189707356 --json
  librarian inspect --mms 991054603339707356 --detail --json   # journal holdings`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if mms == "" {
				if len(args) > 0 {
					mms = args[0]
				} else {
					return fmt.Errorf("an MMS-ID is required: librarian inspect --mms <MMS-ID>")
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
			var services *bsb.TitleServices
			var detailSvc *bsb.TitleServices
			var svcErr string
			var physSvcID string
			if loggedIn {
				// Physical service id first (browser: getPhysicalService before
				// titleServices detail). Empty when nothing is requestable.
				if id, err := client.GetPhysicalService(doc.DocILSID(), doc.SourceRecordID(), doc.ResourceType(), doc.Delivery.RecordOwner); err == nil {
					physSvcID = id
				}
				if s, err := client.TitleServices(doc.MMS()); err != nil {
					svcErr = err.Error()
				} else {
					services = s
					if detail {
						// Prefer the physicalServiceId level (carries the real
						// OvP request links); fall back to the base serviceId.
						svcID := s.ServiceID
						if physSvcID != "" {
							svcID = physSvcID
						}
						if d, err := client.TitleServicesDetail(doc.MMS(), svcID); err != nil {
							svcErr = err.Error()
						} else {
							detailSvc = d
						}
					}
				}
			} else if detail {
				return fmt.Errorf("--detail requires login: run `librarian auth login` first")
			}
			if flagJSON {
				out := map[string]any{
					"mms":                 doc.MMS(),
					"title":               doc.Title(),
					"creators":            shortList(append(doc.Pnx.Display.Creator, doc.Pnx.Display.Contributor...), 5),
					"year":                firstOf(doc.Pnx.Display.CreationDate),
					"publisher":           firstOf(doc.Pnx.Display.Publisher),
					"types":               doc.Pnx.Display.Type,
					"formats":             doc.Pnx.Display.Format,
					"languages":           doc.Pnx.Display.Language,
					"isbn":                doc.Pnx.Addata.ISBN,
					"subjects":            shortList(doc.Pnx.Display.Subject, 8),
					"categories":          doc.Delivery.DeliveryCategory,
					"service_mode":        doc.Delivery.ServiceMode,
					"holdings":            doc.Delivery.Holding,
					"online":              doc.OnlineLinks(),
					"services":            services,
					"physical_service_id": physSvcID,
					"request_path":        bsb.RequestPath(doc),
					"logged_in":           loggedIn,
				}
				if detailSvc != nil {
					out["detail"] = detailSvc
				}
				return printJSON(out)
			}
			d := doc.Pnx.Display
			fmt.Printf("%s\n", doc.Title())
			fmt.Printf("MMS: %s | Type: %s | Year: %s\n", doc.MMS(), strings.Join(d.Type, ", "), firstOf(d.CreationDate))
			if c := shortList(append(d.Creator, d.Contributor...), 5); len(c) > 0 {
				fmt.Printf("By: %s\n", strings.Join(c, "; "))
			}
			if p := firstOf(d.Publisher); p != "" {
				fmt.Printf("Publisher: %s\n", p)
			}
			if len(d.Format) > 0 {
				fmt.Printf("Format: %s\n", strings.Join(d.Format, "; "))
			}
			if len(doc.Pnx.Addata.ISBN) > 0 {
				fmt.Printf("ISBN: %s\n", strings.Join(doc.Pnx.Addata.ISBN, ", "))
			}
			fmt.Printf("\nMedia / availability (categories: %s | mode: %s):\n", strings.Join(doc.Delivery.DeliveryCategory, ", "), strings.Join(doc.Delivery.ServiceMode, ", "))
			fmt.Printf("  request path: %s\n", bsb.RequestPath(doc))
			if physSvcID != "" {
				fmt.Printf("  physical_service_id: %s\n", physSvcID)
			}
			printHoldings(doc)
			printOnline(doc)
			if services != nil {
				fmt.Printf("\nRequest options (logged in):\n")
				for _, s := range services.Services {
					fmt.Printf("  - %s [%s] allowed=%s\n", s.Type, s.ServiceType, s.Allowed)
				}
				if svcErr == "" && len(services.Services) == 0 {
					fmt.Println("  (no request options returned)")
				}
			} else if !loggedIn {
				fmt.Printf("\nRequest options: log in first (`librarian auth login`).\n")
			} else if svcErr != "" {
				fmt.Printf("\nRequest options unavailable: %s\n", svcErr)
			}
			if detailSvc != nil {
				printDetail(detailSvc)
			}
			fmt.Printf("\nNext:\n")
			fmt.Printf("  preview the loan chain: librarian borrow --mms %s [--pickup <id>] [--json]\n", doc.MMS())
			fmt.Printf("  place it for real:      librarian borrow --mms %s --yes [--pickup <id>]\n", doc.MMS())
			return nil
		},
	}
	cmd.Flags().StringVar(&mms, "mms", "", "Alma MMS-ID of the record (from research)")
	cmd.Flags().BoolVar(&detail, "detail", false, "fetch per-service detail: holdings summaries, copy statements, year/volume filters (login required)")
	return cmd
}

func firstOf(in []string) string {
	if len(in) > 0 {
		return in[0]
	}
	return ""
}

func printHoldings(doc *bsb.Doc) {
	if len(doc.Delivery.Holding) == 0 {
		fmt.Println("  physical: no copy information in this view")
		return
	}
	for _, h := range doc.Delivery.Holding {
		fmt.Printf("  physical: %s — %s / %s, shelfmark %s [%s]\n",
			h.MainLocation, h.SubLocation, h.LibraryCode, h.CallNumber, h.AvailabilityStatus)
	}
}

func printOnline(doc *bsb.Doc) {
	links := doc.OnlineLinks()
	if len(links) == 0 {
		fmt.Println("  digital: no online links")
		return
	}
	for _, l := range links {
		fmt.Printf("  digital: [%s] %s\n           %s\n", l.LinkType, l.DisplayLabel, l.LinkURL)
	}
}

// printDetail renders the titleServices detail level: per-location holdings
// summaries (journal coverage), copy statements and year/volume filters.
func printDetail(d *bsb.TitleServices) {
	fmt.Printf("\nHoldings detail (service %s):\n", d.ServiceID)
	for _, l := range d.Locations {
		head := l.CallNumber
		if head == "" {
			head = "(no shelfmark)"
		}
		fmt.Printf("  %s — %s / %s, shelfmark %s\n", l.MainLocation, l.SubLocation, l.LibraryCode, head)
		if l.AvailabilityStatement != "" {
			fmt.Printf("    copies: %s\n", strings.Trim(l.AvailabilityStatement, "()"))
		}
		for key, vals := range l.Summaries {
			fmt.Printf("    %s:\n", key)
			for _, v := range vals {
				fmt.Printf("      %s\n", v)
			}
		}
		for _, it := range l.Items {
			fmt.Printf("    item %s: status=%s requestable=%s\n", it.Barcode, it.Status, it.Requestable)
		}
	}
	if len(d.Filters.Years) > 0 || len(d.Filters.Volumes) > 0 {
		fmt.Printf("  year/volume filter: years=%v volumes=%v descriptions=%v\n",
			d.Filters.Years, d.Filters.Volumes, d.Filters.Descriptions)
	}
	for _, s := range d.RapidoServices {
		fmt.Printf("  resource-sharing: %s [%s] allowed=%s\n", s.Type, s.ServiceType, s.Allowed)
	}
}
