-- The extracted text of every document in the archive.
--
-- READ THE TRADE BEFORE READING THE SCHEMA. This text is encrypted at rest
-- today, on purpose: it holds supplier names, prices, quantities and contract
-- terms, and it was plain bytes on disk until that was fixed. The cache it
-- lives in is AES-256-GCM under a DPAPI-wrapped, machine-bound key.
--
-- Putting it here trades that key for this database's access control. What
-- carries the at-rest job afterwards is: PostgreSQL listening on loopback
-- only, the docfind role having no rights outside its own schema, and DISK
-- ENCRYPTION ON .13 BECOMING LOAD-BEARING. That last one is not yet true --
-- .13 has no BitLocker feature installed and is a VMware guest with no TPM, so
-- it needs vTPM or VM Encryption on the host. Until then this table holds
-- readable business text on an unencrypted volume, which is why writing to it
-- is OFF unless DOCFIND_PG_DOCTEXT=1 is set deliberately. The feature is built;
-- switching it on is a decision with a prerequisite.
--
-- WHY IT IS WORTH IT ANYWAY, and this is a stronger argument than "search":
-- the current cache is machine-bound, so it cannot be usefully backed up and
-- is rebuilt from nothing if the box is. PDF extraction is the measured
-- throughput ceiling -- 18.2 documents/second at 40% PDFs against 232 for an
-- all-Word archive -- so a rebuild is hours. With the text here, no page is
-- ever OCR'd twice.
--
-- POSTGRESQL IS THE STORE, NOT THE QUERY PATH. The search is already a real
-- in-process inverted index (postings, vocab, vocabByLen); replacing it with
-- SQL would trade that for a database round trip per keystroke. Text is read
-- from here at start-up to build that index, and written back as documents are
-- extracted.

CREATE TABLE IF NOT EXISTS documents (
    -- The archive-relative path, which is what the index keys on.
    path              TEXT PRIMARY KEY,

    -- What the file looked like when it was read. Re-extraction is triggered
    -- by any of these changing, never by a blind full rescan: rescanning
    -- 4,500 documents to find the six that changed is the cost this avoids.
    size_bytes        BIGINT NOT NULL,
    modified_at       TIMESTAMPTZ NOT NULL,
    -- Nullable: hashing every file is a second full read of the archive, so
    -- it is only filled where something needed to be sure. Size and mtime
    -- together are enough to spot an edit in practice.
    content_sha256    TEXT,

    -- extractor_version lets a better OCR pass deliberately invalidate every
    -- row. Without it, improving extraction would mean either trusting stale
    -- text for ever or dropping the table by hand.
    extractor_version INTEGER NOT NULL DEFAULT 1,
    extracted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- HOW THE TEXT WAS OBTAINED. None of these can be backfilled -- working
    -- them out later means extracting the archive again, which is the
    -- expensive operation this table exists to avoid repeating.
    --
    -- ocr_used: tesseract produced this, not an embedded text layer. Embedded
    -- text is exact and OCR text contains errors, so ranking should not treat
    -- them as equal.
    ocr_used          BOOLEAN NOT NULL DEFAULT FALSE,
    -- ocr_truncated and pages_read: OCR STOPS AFTER THE FIRST 8 PAGES. A
    -- 40-page scanned contract is indexed from a fifth of itself and a search
    -- for a term on page nine finds nothing -- today, silently. These two
    -- turn that into "we only looked at 8 pages", which is an answer somebody
    -- can act on.
    ocr_truncated     BOOLEAN NOT NULL DEFAULT FALSE,
    pages_read        INTEGER NOT NULL DEFAULT 0,
    -- tools_missing: the text is empty because pdftotext/tesseract were absent
    -- rather than because the document is empty. Without it a re-run cannot
    -- tell a genuinely blank scan from one nobody could read, and would either
    -- re-OCR everything for ever or skip the readable ones.
    tools_missing     BOOLEAN NOT NULL DEFAULT FALSE,

    body              TEXT NOT NULL DEFAULT '',
    -- body put through internal/fa.Fold, because PostgreSQL ships NO PERSIAN
    -- STEMMER and pretending otherwise is the main way this gets built wrong.
    -- The fold normalises the two kafs, the three yehs, heh/teh-marbuta, the
    -- alef family, both digit sets, kashida and ZWNJ. Written by Go, since the
    -- fold is Go's, and the same fold is applied to the query.
    body_folded       TEXT NOT NULL DEFAULT ''
);

-- Finding what needs extracting, which is the only query run over this table
-- at any volume.
CREATE INDEX IF NOT EXISTS documents_stale
    ON documents (extractor_version, extracted_at);

-- Deliberately NO full-text index and no tsvector column.
--
-- The in-memory inverted index answers the searches; a GIN index here would be
-- machinery nobody queries, paid for on every write during a 4,500-document
-- scan. If the query path ever moves, body_folded is exactly what it would go
-- on -- which is why the fold is stored rather than computed on read.
