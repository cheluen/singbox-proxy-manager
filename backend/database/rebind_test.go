package database

import "testing"

func TestQMarkToPostgres(t *testing.T) {
	input := `
		SELECT '?' AS literal, "?" AS ident, col
		FROM table_name
		WHERE a = ? AND b = ? -- ? comment
		  AND c = 'it''s ? fine'
		  AND d = $$ ? dollar $$
		  AND e = $tag$ ? tagged $tag$
		  /* ? block */
		  AND f = ?
	`
	got := QMarkToPostgres(input)
	want := `
		SELECT '?' AS literal, "?" AS ident, col
		FROM table_name
		WHERE a = $1 AND b = $2 -- ? comment
		  AND c = 'it''s ? fine'
		  AND d = $$ ? dollar $$
		  AND e = $tag$ ? tagged $tag$
		  /* ? block */
		  AND f = $3
	`
	if got != want {
		t.Fatalf("unexpected rewrite:\n%s", got)
	}
}
