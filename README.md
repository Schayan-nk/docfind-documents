# docfind-documents

A self-hosted document archive written in Go. Point it at a folder of Word,
Excel, PDF and scanned files: it indexes them as they change, searches every
word inside them in Persian and English, shows them in the browser, and lets
the people allowed to add, file and edit documents without leaving the page.
With an ONLYOFFICE Document Server beside it, Word files are edited in place
and the version each save replaces is kept.

It began as the archive inside DocFind, a document and workflow system, and
runs here on its own: one binary and a folder. PostgreSQL is optional.

## What it does

**Indexing**
- Word (.docx) and Excel (.xlsx) are read by the binary itself; PDFs through
  pdftotext; scans, photos and PDFs with no text layer through Tesseract OCR,
  when those tools are installed
- The folder is watched on Windows and rescanned on a timer everywhere
  (`-rescan`), so documents copied in by any other means show up
- The index is cached between runs, encrypted at rest. Optionally the
  extracted text is kept in PostgreSQL, so a rescan never runs OCR twice

**Search**
- Every word in every document, with Persian folded first: Arabic and
  Persian letter forms, digits, zero-width joiners and short vowels
- Order, invoice, reference and tender numbers recognised however they are
  written, and the document that carries one put first and marked exact
- A question mode that reads the box as Persian (a document type, a Jalali
  month or year, a reference number) and shows exactly what it understood
- A folder tree by reference number, filters by type and category, and
  Ctrl+K from any page

**Reading**
- Word documents rendered as they look (docx-preview) or as plain text; PDFs
  and images in the browser
- A facts strip above each document: its amounts, payment terms, reference
  numbers and dates, each a piece of its own text; a reference finds the
  other documents that carry it

**Changing the archive**
- Uploads through a drop zone. Each file is checked in a quarantine folder
  first (by Microsoft Defender where it is installed), and only office
  documents, PDFs, images, text, mail files (.msg, .eml) and zip archives are
  accepted
- Editing in Word through ONLYOFFICE, with the replaced version kept and
  restorable
- New folders, renaming and deleting, held to named accounts (see
  Permissions)

**Accounts and security**
- Roles (administrator, manager, viewer) with per-account allow and deny
  overrides
- Two-step sign-in with an authenticator app, a code by email or by SMS, and
  passkeys (WebAuthn)
- A sign-in policy (password rules, lockouts, two steps for everyone), the
  list of active sessions, and an audit trail of every change
- A Content-Security-Policy that runs scripts from this server only (the
  pages' own inline scripts included), CSRF and origin checks, rate limits,
  and an archive confined to its folder: links and `..` cannot reach outside
  it

## How the folder is read

Documents are filed by reference number. The digit folders above a file make
its reference, the first three digits its group, and the first part of its
name its type:

```
406/300/Proforma Invoice_406300.docx    reference 406300, group 406, type "Proforma Invoice"
```

An archive filed differently still works: a file with no digit folders takes
its reference from the digits in its name, and everything is searchable by
its text either way. The question mode knows the document types an archive
of commercial papers has (inquiries, offers, proformas, orders, confirmations,
packing lists, invoices, contracts); the rest of a question is searched as
words.

## Requirements

- Go 1.26 to build
- poppler (pdftotext, pdftoppm) for PDFs, and Tesseract with the Persian and
  English language data for scans. Optional: without them only Word, Excel
  and text files are read. They are found on the PATH, beside the binary, in
  a `tools` folder beside it, or in the usual Windows install folders
- PostgreSQL (optional) for accounts, the searchable audit trail, an editable
  sign-in policy and role templates, and the extracted text. Without it
  accounts are kept in the accounts file and the defaults apply
- An ONLYOFFICE Document Server (optional) for editing in the browser
- HTTPS in front of it for anything beyond this machine: passkeys and the
  installed app need a secure page. Use a reverse proxy, or give the server a
  certificate with `DOCFIND_TLS_CERT` and `DOCFIND_TLS_KEY`

## Quick start

```sh
go build -o docfind-documents ./cmd/documents

cp .env.example .env        # then set DOCFIND_ARCHIVE at least

# The first administrator, typed at the terminal (the password is not echoed):
./docfind-documents -users data/users.json -add-user admin -role admin

# On this machine only, to try it:
./docfind-documents -users data/users.json -archive /srv/archive

# For the network, once an account exists:
./docfind-documents -users data/users.json -bind 0.0.0.0 -port 8310
```

The server refuses to listen beyond this machine while the accounts file is
empty, so the first administrator cannot be created over the network by
whoever gets there first. If the folder cannot be read, the server still
starts and its page says why; an administrator can name another folder there.

## Permissions

| Permission | Who has it by default |
|---|---|
| `page:documents` (search and read) | everyone |
| `action:docs_write` (edit in Word) | everyone |
| `action:docs_upload`, `action:reindex` | managers and administrators |
| `action:docs_edit` (new folders, rename, move), `action:docs_delete` | nobody: granted per account |
| `action:manage_users`, `action:manage_settings` | administrators |

Renaming and deleting change a tree with no recycle bin, so no role carries
them, not even an administrator's. Grant them to the accounts that should
have them in Settings (nobody may change their own permissions, so that takes
a second administrator), or from the command line with the server stopped,
which works with or without PostgreSQL:

```sh
./docfind-documents -users data/users.json -set-grant admin -perm action:docs_delete -state allow
```

## Configuration

Everything is read from the environment, or from a `.env` file beside the
binary (`-env-file` to name another). Nothing secret belongs in the code or in
a config file under version control. [.env.example](.env.example) lists every
variable with what it does; the main ones:

| Variable | Purpose |
|---|---|
| `DOCFIND_ARCHIVE` | The folder of documents (or `-archive`). |
| `DOCFIND_PG_HOST`, `DOCFIND_PG_PORT`, `DOCFIND_PG_USER`, `DOCFIND_PG_PASSWORD`, `DOCFIND_PG_DATABASE`, `DOCFIND_PG_SSLMODE` | The optional database. The schema is created and upgraded on start. |
| `DOCFIND_PG_DOCTEXT` | `1` keeps extracted text in PostgreSQL. It is stored there unencrypted. |
| `DOCFIND_OFFICE_URL` | The ONLYOFFICE Document Server; editing is off without it. |
| `DOCFIND_PASSKEY_RPID`, `DOCFIND_PASSKEY_ORIGINS` | The host name passkeys are bound to, and the origins they are used from. |

Files the server keeps beside the accounts file (`-users`): the session
signing key, the active sessions, profile pictures, the key that seals
two-step secrets, the upload quarantine, the editor's secret and the
versions it replaced, and by default the logs. Back that folder up with the
database. The index cache is kept in the user's cache folder.

## Command line

| Flag | |
|---|---|
| `-users <file>` | The accounts file. Required. |
| `-archive <folder>` | The documents. Default: `DOCFIND_ARCHIVE`. |
| `-rescan <duration>` | How often to look for documents added from outside. Default 30s. |
| `-bind <address>`, `-port <n>` | Where to listen. Default 127.0.0.1:8310. |
| `-trust-proxy-headers` | Take the client address from X-Forwarded-For. Only behind a proxy you control. |
| `-add-user <name> -role admin\|manager\|viewer` | Create an account, or reset its password, then exit. |
| `-set-grant <name> -perm <id> -state allow\|deny\|inherit` | Set one permission override, with the server stopped. |
| `-set-phone <name> -phone <number>` | Set an account's mobile number in PostgreSQL, with the server stopped. Without a database, set it in Settings. |
| `-env-file <path>`, `-env-file-wins` | Which `.env` to read, and whether it overrides the real environment. |
| `-version` | Print the version. |

## Layout

| Path | |
|---|---|
| `cmd/documents` | The binary: options, start-up, the archive folder, the editor's wiring |
| `internal/find` | The index, the search, the HTTP server and its pages |
| `internal/docxhtml`, `internal/ooxml`, `internal/xmltree` | Reading Word files and laying them out |
| `internal/onlyoffice` | The Document Server's signed configuration and callbacks |
| `internal/auth` | Accounts, sessions, roles, permissions, the sign-in policy |
| `internal/pgstore` | PostgreSQL access and the numbered migrations |
| `internal/webauthn` | Passkeys |
| `internal/audit`, `internal/obs` | The audit trail, request logs and metrics |
| `internal/fa`, `internal/bidi`, `internal/format` | Persian text, direction and the Jalali calendar |

## Development

```sh
go vet ./...
go test ./...
```

The tests build small archives of real .docx files in temporary folders and
run the real router against them. The tests that need PostgreSQL run when
`DOCFIND_PG_*` points at a test database and skip otherwise. Use one you can
throw away, and run them one package at a time (`go test -p 1 ./...`).

## License

MIT. See [LICENSE](LICENSE). The bundled third-party files keep their own
licences; see [THIRD-PARTY.md](THIRD-PARTY.md).
