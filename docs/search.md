# Search Notes

Initial search supports regex filtering across title, body, and tags.

## Ideas
- Add FTS index per backend (SQLite FTS5, Postgres GIN/TSVector).
- Consider portable scoring for cross-backend consistency.
- Highlight matches with context lines.

