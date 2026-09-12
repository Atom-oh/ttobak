# Current legacy text evidence

The complete QA wiring review found that replacing unbound index chunks with
the current file head discarded matching facts after the first 800 characters.
This fixes the reader/formatter contract before that wiring is deployed.

- Read current scoped text with the existing source reader's ETag/version
  guards. Accept an indexed excerpt only when it exists in current text.
- Consider duplicate URI candidates together; preserve the best verified
  matching excerpt rather than whichever candidate arrives first.
- If the indexed text is stale, locate an excerpt in current text instead.
  Never reuse stale text as an availability fallback.
- Provide source-relative coverage and revision-bound continuation through
  `get_legacy_text_detail`; keep full-text and binary reads distinct.
- Keep the current public QA handler unchanged in this prerequisite. The
  prepared wiring PR adds the matching authenticated callback.

Validation uses synthetic current/stale text, mid-file facts, duplicate hits,
suffix continuation, foreign URI denial and changed source versions through
the canonical `python3 -m unittest test_handler -v` suite.
