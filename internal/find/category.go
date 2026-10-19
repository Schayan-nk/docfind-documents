package find

import (
	"sort"
	"strings"
)

// Document categories.
//
// The archive stores what the generator called a file -- Anfrage_333444.docx,
// Pi_NO_111222.docx -- which is precise but not what anyone browsing for a
// document would look under. This maps those raw prefixes onto the names the
// business actually uses, so the tree reads "Inquiry" and "Proforma Invoice".
//
// Matching is on the type prefix taken from the file name, lower-cased. The
// first rule whose alias matches wins, so more specific aliases are listed
// before shorter ones that could also match.
var categoryRules = []struct {
	Aliases []string
	Name    string
}{
	{[]string{"inquiry", "anfrage", "enquiry", "rfq"}, "Inquiry"},
	{[]string{"techoffer", "technicaloffer", "technical", "techoff", "to"}, "Technical Offer"},
	{[]string{"pi_no", "pino", "proforma", "proformainvoice", "pi"}, "Proforma Invoice"},
	{[]string{"purchaseorder", "purchase_order", "bestellung", "po", "order"}, "Purchase Order"},
	{[]string{"packinglist", "packing_list", "packing", "pl"}, "Packing List"},
	{[]string{"invoice", "inv", "rechnung"}, "Invoice"},
	{[]string{"contract", "vertrag", "agreement"}, "Contract"},
	{[]string{"orderconfirm", "orderconfirmation", "confirmation", "oc"}, "Order Confirmation"},
	{[]string{"quotation", "quote", "angebot"}, "Quotation"},
}

// CategoryOrder is the order categories appear in the browser, following the
// order a job actually moves through rather than the alphabet.
var CategoryOrder = []string{
	"Inquiry",
	"Technical Offer",
	"Quotation",
	"Proforma Invoice",
	"Purchase Order",
	"Order Confirmation",
	"Packing List",
	"Invoice",
	"Contract",
	"Other",
}

// categoryAlias is one (alias, category) pair flattened out of categoryRules,
// used for the prefix fallback below.
type categoryAlias struct {
	alias string
	name  string
}

// categoryPrefixes holds every alias sorted longest-first, so the prefix
// fallback in CategoryOf tries the most specific alias before a shorter,
// more general one that happens to also match -- e.g. "orderconfirm" (Order
// Confirmation) before "order" (Purchase Order), regardless of which rule
// comes first in categoryRules. Sorting by rule order instead of alias length
// let the generic "order" alias swallow anything starting with "order",
// including abbreviated Order Confirmation file names.
var categoryPrefixes = func() []categoryAlias {
	var out []categoryAlias
	for _, rule := range categoryRules {
		for _, a := range rule.Aliases {
			if len(a) >= 2 {
				out = append(out, categoryAlias{a, rule.Name})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].alias) > len(out[j].alias) })
	return out
}()

// CategoryOf maps a raw file-name type onto a business category.
//
// Anything unrecognised becomes "Other" rather than being hidden, so a new
// document type shows up in the tree the day it first appears instead of
// silently going missing.
func CategoryOf(rawType string) string {
	key := strings.ToLower(strings.TrimSpace(rawType))
	key = strings.NewReplacer(" ", "", "-", "").Replace(key)
	if key == "" {
		return "Other"
	}

	for _, rule := range categoryRules {
		for _, a := range rule.Aliases {
			if key == a {
				return rule.Name
			}
		}
	}
	// Fall back to a prefix match so Pi_NO_2 or InvoiceFinal still land right.
	// Longest alias first, so a specific alias always beats a shorter, more
	// general one rather than whichever rule happens to be listed first.
	for _, ca := range categoryPrefixes {
		if strings.HasPrefix(key, ca.alias) {
			return ca.name
		}
	}
	return "Other"
}

// categoryRank orders categories by CategoryOrder, with unknown names last.
func categoryRank(name string) int {
	for i, n := range CategoryOrder {
		if n == name {
			return i
		}
	}
	return len(CategoryOrder)
}

// GroupOf returns the folder group a reference belongs to: the first three
// digits, matching how the archive is laid out on disk.
func GroupOf(ref string) string {
	if len(ref) >= 3 {
		return ref[:3]
	}
	if ref == "" {
		return "—"
	}
	return ref
}
