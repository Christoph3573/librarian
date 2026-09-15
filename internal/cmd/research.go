package cmd

import (
	"fmt"
	"html"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Christoph3573/librarian/internal/bsb"
	"github.com/Christoph3573/librarian/internal/session"
)

type researchHit struct {
	MMS        string   `json:"mms"`
	Title      string   `json:"title"`
	Creators   []string `json:"creators,omitempty"`
	Year       string   `json:"year,omitempty"`
	Publisher  string   `json:"publisher,omitempty"`
	Types      []string `json:"types,omitempty"`
	Formats    []string `json:"formats,omitempty"`
	Categories []string `json:"delivery_categories,omitempty"`
}

func newResearchCmd() *cobra.Command {
	var field string
	var limit, offset int
	var delivery bool
	var include, exclude []string
	var withFacets bool
	cmd := &cobra.Command{
		Use:   "research <query>",
		Short: "Search the BSB catalog for books/media",
		Long: `Search the BSB OPAC+ catalog and list available media (title, MMS-ID, formats).

Works anonymously (no login needed). If logged in, the saved session is sent
along so restricted availability is resolved for your account.

Query syntax: free text is matched against all fields. Use --field to narrow
(title|creator|subject|isbn|issn|any). The MMS-ID in the output identifies a
record for 'inspect' and 'borrow'.

Facet filters narrow the result set like the web UI facets. Repeatable:
  --include rtype:books --include tlevel:open_access
  --exclude rtype:newspaperarticle
Known facets: rtype (books, articles, journals, dissertations, ...),
tlevel (available_p, open_access, online_resources, peer_reviewed),
creationdate (e.g. 2020), creator, language, ... Use --facets to show the
aggregation buckets with hit counts for your query.

AGENT HINT: run 'librarian research --help' first. Prefer --json and keep
--limit small (5-10). Then call 'librarian inspect --mms <MMS-ID>' per hit.

Examples:
  librarian research "kafka faust"
  librarian research --field title --limit 5 "faust"
  librarian research --json --limit 5 "goethe werther"
  librarian research --delivery --json "kafka"   # include availability
  librarian research --include rtype:books --json "test"
  librarian research --facets --limit 1 --json "test"   # facet buckets`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")
			client := bsb.NewClient(flagView, flagLang, "")
			if sess, err := session.Load(); err == nil && !sess.Expired() {
				client.JWT = sess.JWT
			}
			res, err := client.Search(bsb.SearchParams{
				Query: query, Field: field,
				Limit: limit, Offset: offset, WithDelivery: delivery,
				IncludeFacets: include, ExcludeFacets: exclude,
				WithFacets: withFacets,
			})
			if err != nil {
				return err
			}
			hits := make([]researchHit, 0, len(res.Docs))
			for _, d := range res.Docs {
				creators := append([]string{}, d.Pnx.Display.Creator...)
				creators = append(creators, d.Pnx.Display.Contributor...)
				year := ""
				if len(d.Pnx.Display.CreationDate) > 0 {
					year = d.Pnx.Display.CreationDate[0]
				}
				pub := ""
				if len(d.Pnx.Display.Publisher) > 0 {
					pub = d.Pnx.Display.Publisher[0]
				}
				hits = append(hits, researchHit{
					MMS: d.MMS(), Title: d.Title(),
					Creators:   shortList(creators, 3),
					Year:       year,
					Publisher:  pub,
					Types:      d.Pnx.Display.Type,
					Formats:    d.Pnx.Display.Format,
					Categories: d.Delivery.DeliveryCategory,
				})
			}
			if flagJSON {
				out := map[string]any{
					"query":         query,
					"total":         res.Info.Total,
					"total_local":   res.Info.TotalResultsLocal,
					"total_central": res.Info.TotalResultsPC,
					"hits":          hits,
				}
				if withFacets {
					out["facets"] = res.Facets
					out["highlights"] = res.Highlights
				}
				return printJSON(out)
			}
			fmt.Printf("%d hits total (%d local) for %q:\n", res.Info.Total, res.Info.TotalResultsLocal, query)
			for i, h := range hits {
				fmt.Printf("\n[%d] %s\n    MMS: %s | Type: %s\n", i+1, h.Title, h.MMS, strings.Join(h.Types, ", "))
				if len(h.Creators) > 0 {
					fmt.Printf("    By: %s", strings.Join(h.Creators, "; "))
					if h.Year != "" {
						fmt.Printf(" (%s)", h.Year)
					}
					fmt.Println()
				}
				if h.Publisher != "" {
					fmt.Printf("    Publisher: %s\n", h.Publisher)
				}
			}
			fmt.Printf("\nNext: librarian inspect --mms <MMS-ID> [--json]\n")
			if withFacets {
				printFacets(res.Facets)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&field, "field", "any", "search field: any|title|creator|subject|isbn|issn")
	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "max records to show (1-50)")
	cmd.Flags().IntVar(&offset, "offset", 0, "paging offset")
	cmd.Flags().BoolVar(&delivery, "delivery", false, "include availability/delivery info (slower)")
	cmd.Flags().StringSliceVar(&include, "include", nil, "facet filter name:value, repeatable (e.g. rtype:books)")
	cmd.Flags().StringSliceVar(&exclude, "exclude", nil, "facet exclusion name:value, repeatable")
	cmd.Flags().BoolVar(&withFacets, "facets", false, "show facet buckets + highlights for the query")
	return cmd
}

// printFacets renders facet buckets (top values with counts) for humans.
func printFacets(facets []bsb.Facet) {
	if len(facets) == 0 {
		return
	}
	fmt.Printf("\nFacets (use --include <name>:<value>):\n")
	for _, f := range facets {
		if len(f.Values) == 0 {
			continue
		}
		var parts []string
		for i, v := range f.Values {
			if i >= 6 {
				break
			}
			parts = append(parts, fmt.Sprintf("%v (%v)", v.Value, v.Count))
		}
		fmt.Printf("  %s: %s\n", f.Name, strings.Join(parts, ", "))
	}
}

func shortList(in []string, n int) []string {
	var out []string
	for _, s := range in {
		if i := strings.Index(s, "$$"); i >= 0 {
			s = s[:i]
		}
		// Primo separates name/role/authority with " &#124 " (escaped pipe).
		if i := strings.Index(s, "&#124"); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
		s = html.UnescapeString(strings.TrimSpace(s))
		if s != "" {
			out = append(out, s)
		}
		if len(out) >= n {
			break
		}
	}
	return out
}
