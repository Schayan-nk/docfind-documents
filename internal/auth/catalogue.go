package auth

// Every grantable thing in the application, declared once.
//
// WHAT WAS WRONG WITH THE OLD MODEL. Five permissions mapped from three roles
// in a table in this file, and that table WAS the access model. It cannot say
// "this person may do what a manager does, except this one thing", which is
// the thing that was asked for. Measured at the time: of 133 registered
// routes, 9 consulted a permission and 51 were gated on nothing but "is signed
// in".
//
// THE ID IS THE CONTRACT AND THE LABEL IS DECORATION. Grants are stored
// against the ID, so renaming an entry in Persian never silently revokes
// anybody's access to it -- which is exactly what would happen if the label
// were the key.
//
// ANYTHING NOT IN HERE IS DENIED, to everyone but an administrator. That rule
// is the one that earns its place: it means adding a route cannot accidentally
// grant access to it. Forgetting to declare a permission fails closed and
// somebody complains; the opposite fails open and nobody notices.

// Kind groups the catalogue for the screen that edits it, and nothing else.
type Kind string

const (
	// KindPage is a whole screen.
	KindPage Kind = "page"
	// KindAction is something done rather than seen.
	KindAction Kind = "action"
)

// Item is one grantable thing.
type Item struct {
	ID    string `json:"id"`
	Kind  Kind   `json:"kind"`
	Group string `json:"group"`
	Label string `json:"label"`
	// AdminOnly marks an item no template but admin's may hold and no
	// override may grant. Reserved for the things that would let somebody
	// grant themselves everything else.
	AdminOnly bool `json:"admin_only,omitempty"`
	// Explicit marks an item NO TEMPLATE GIVES -- not even an
	// administrator's. It is held only by an explicit allow on the account.
	//
	// THIS EXISTS BECAUSE SOME THINGS BELONG TO PEOPLE, NOT TO A ROLE: "these
	// two, and no one else, not even other administrators" is about identity,
	// and a catalogue whose rule is "an administrator holds everything
	// declared" would quietly hand such an item to every administrator.
	//
	// So: Explicit is how the catalogue says "belongs to named people". It
	// composes with the other rules rather than replacing them -- a deny
	// still beats it, and an administrator can still GRANT it to somebody
	// (including themselves, through another administrator), which is the
	// difference between Explicit and AdminOnly.
	Explicit bool `json:"explicit,omitempty"`
}

// The page IDs. Named constants because they are referenced from route
// registrations, and a typo in a string literal there would deny a page to
// everybody -- or, if it were the other way round, grant it.
const (
	PageDocuments = "page:documents"
	PageSettings  = "page:settings"
)

// The actions.
const (
	ActionManageUsers    = "action:manage_users"
	ActionManageSettings = "action:manage_settings"

	// Adding to the archive: a new document, and a new version of one from
	// the Word editor (the one it replaces is kept). Re-reading the whole
	// archive into the index is the third.
	ActionDocsUpload = "action:docs_upload"
	ActionDocsWrite  = "action:docs_write"
	ActionReindex    = "action:reindex"

	// Changing what is already there: renaming or moving, and deleting. In a
	// tree with no recycle bin, holding documents people are obliged to
	// keep, so both are Explicit -- held by named accounts, not by a role.
	ActionDocsEdit   = "action:docs_edit"
	ActionDocsDelete = "action:docs_delete"
)

// Persian group headings. The editing screen shows the catalogue grouped.
const (
	groupDocuments = "اسناد"
	groupGeneral   = "عمومی"
	groupAdmin     = "مدیریت"
)

// builtinItems is everything that can be granted. The labels are the words
// on the screen, so the permission for a page is spelled the way the person
// granting it sees it in the menu.
var builtinItems = []Item{
	{PageDocuments, KindPage, groupDocuments, "اسناد", false, false},
	{ActionDocsUpload, KindAction, groupDocuments, "بارگذاری سند", false, false},
	{ActionDocsWrite, KindAction, groupDocuments, "ویرایش سند در Word (نسخهٔ پیشین نگه داشته می‌شود)", false, false},
	{ActionDocsEdit, KindAction, groupDocuments, "تغییر نام و پوشه", false, true},
	{ActionDocsDelete, KindAction, groupDocuments, "حذف سند", false, true},
	{ActionReindex, KindAction, groupDocuments, "بازسازی فهرست اسناد", false, false},
	{PageSettings, KindPage, groupGeneral, "تنظیمات", false, false},
	{ActionManageUsers, KindAction, groupAdmin, "مدیریت کاربران و دسترسی‌ها", true, false},
	{ActionManageSettings, KindAction, groupAdmin, "تغییر تنظیمات سامانه", true, false},
}

// Catalogue is the set of grantable things for this deployment.
//
// Immutable once built, because it is read by every request on every
// goroutine: mutating it in place would be a data race on the access model,
// which is the worst place to have one.
type Catalogue struct {
	items []Item
	byID  map[string]Item
}

// NewCatalogue returns the built-in catalogue.
func NewCatalogue() *Catalogue { return buildCatalogue(builtinItems) }

func buildCatalogue(items []Item) *Catalogue {
	c := &Catalogue{items: items, byID: make(map[string]Item, len(items))}
	for _, it := range items {
		c.byID[it.ID] = it
	}
	return c
}

// Has reports whether an ID is declared.
func (c *Catalogue) Has(id string) bool {
	if c == nil {
		return false
	}
	_, ok := c.byID[id]
	return ok
}

// Item returns one entry.
func (c *Catalogue) Item(id string) (Item, bool) {
	if c == nil {
		return Item{}, false
	}
	it, ok := c.byID[id]
	return it, ok
}

// IDs returns every declared ID.
func (c *Catalogue) IDs() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.items))
	for _, it := range c.items {
		out = append(out, it.ID)
	}
	return out
}

// Group is one heading and its items, for the editing screen.
type Group struct {
	Name  string `json:"name"`
	Items []Item `json:"items"`
}

// Groups returns the catalogue grouped, in the order groups first appear.
func (c *Catalogue) Groups() []Group {
	if c == nil {
		return nil
	}
	var order []string
	byName := map[string][]Item{}
	for _, it := range c.items {
		if _, seen := byName[it.Group]; !seen {
			order = append(order, it.Group)
		}
		byName[it.Group] = append(byName[it.Group], it)
	}
	out := make([]Group, 0, len(order))
	for _, name := range order {
		out = append(out, Group{Name: name, Items: byName[name]})
	}
	return out
}
