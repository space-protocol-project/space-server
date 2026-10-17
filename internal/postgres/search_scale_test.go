package postgres

import (
	"strings"
	"testing"
	"time"
	"unicode"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

func TestSearchAndHistory100000(t *testing.T) {
	f := newChannelFixtureTimeout(t, 3*time.Minute)
	// Только изолированная тестовая схема: готовим исходное состояние версии 20.
	_, err := f.store.pool.Exec(f.ctx, `DROP INDEX contents_search_terms_v1;
 ALTER TABLE contents DROP COLUMN search_text;
 DROP FUNCTION space_search_terms_v1(text,int,int); DROP FUNCTION space_search_fold_v1(text);
 DELETE FROM schema_migrations WHERE version=21;`)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = f.store.pool.Exec(f.ctx, `INSERT INTO contents(channel_id,sequence,id,text,idempotency_key,author_id,request_text_hash)
 SELECT 'general',n,'message-'||n,body,'scale-'||n,$1,sha256(convert_to(body,'UTF8'))
 FROM generate_series(1,100000) n CROSS JOIN LATERAL
 (SELECT CASE WHEN n%19999=0 THEN 'РЕДКИЙМАРКЕР '||n ELSE 'Обычная история сообщение '||n END AS body) b`, f.ownerID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.pool.Exec(f.ctx, `INSERT INTO events(channel_id,sequence,content_sequence,cursor,type)
 SELECT channel_id,sequence,sequence,'event-'||sequence,'content.created' FROM contents;
 UPDATE channels SET next_sequence=100001,next_event_sequence=100001 WHERE id='general';`)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Подготовка 100000 строк и событий: %s", time.Since(start))
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	start = time.Now()
	if err = applyMigration(f.ctx, tx, 21, "021_content_search_index.sql"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("Миграция 20 → 21 с готовой историей: %s", time.Since(start))
	if _, err = f.store.pool.Exec(f.ctx, "ANALYZE contents"); err != nil {
		t.Fatal(err)
	}
	var plan string
	if err = f.store.pool.QueryRow(f.ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+searchContentSQL, "general", int64(0), "редкиймаркер").Scan(&plan); err != nil {
		t.Fatal(err)
	}
	t.Logf("План редкого запроса: %s", plan)
	if !strings.Contains(plan, "contents_search_terms_v1") || strings.Contains(plan, `"Node Type": "Seq Scan"`) {
		t.Fatal("Редкий запрос не использует индекс подстрок")
	}
	start = time.Now()
	matches, err := f.contents.SearchContent(f.owner, &pb.SearchContentRequest{ChannelId: "general", Query: "рЕдКиЙмАрКеР"})
	if err != nil || len(matches.GetContents()) != 5 {
		t.Fatalf("Поиск: %v %v", matches, err)
	}
	t.Logf("Поиск с авторизацией через RPC: %s", time.Since(start))
	// Повторы разных запросов проверяют выбор плана после прогрева prepared statements.
	for attempt := 0; attempt < 12; attempt++ {
		query, want := "история", 50
		if attempt%2 == 0 {
			query, want = "редкиймаркер", 5
		}
		repeated, searchErr := f.contents.SearchContent(f.owner, &pb.SearchContentRequest{ChannelId: "general", Query: query})
		if searchErr != nil || len(repeated.GetContents()) != want {
			t.Fatalf("Повтор поиска %q: %v %v", query, repeated, searchErr)
		}
	}

	reopened, identity, openErr := openManual(f.ctx, f.url)
	if openErr != nil {
		t.Fatal(openErr)
	}
	reopened.Close()
	if identity.ServerID != f.identity.ServerID {
		t.Fatal("Миграция заменила сервер")
	}
	var folded string
	if err = f.store.pool.QueryRow(f.ctx, "SELECT space_search_fold_v1($1)", searchUpper).Scan(&folded); err != nil || folded != searchLower {
		t.Fatalf("Таблица Unicode изменилась: %v", err)
	}
	page, err := f.contents.GetHistoryPage(f.owner, &pb.GetHistoryPageRequest{ChannelId: "general"})
	if err != nil {
		t.Fatal(err)
	}
	boundary := page.EventCursor
	added, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "редкиймаркер новый", IdempotencyKey: "scale-after"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "редкиймаркер новый", IdempotencyKey: "scale-after"})
	if err != nil || replay.Content.Id != added.Content.Id {
		t.Fatal("Повтор создал дубль", err)
	}
	_, err = f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: "message-19999", ExpectedRevision: 1, Text: "Изменено"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.contents.DeleteContent(f.owner, &pb.DeleteContentRequest{ChannelId: "general", Id: "message-39998", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := f.contents.SearchContent(f.owner, &pb.SearchContentRequest{ChannelId: "general", Query: "редкиймаркер"})
	if err != nil || len(changed.GetContents()) != 4 {
		t.Fatal("Индекс не обновился", err)
	}
	events, err := f.sync.ListEvents(f.owner, &pb.ListEventsRequest{ChannelId: "general", After: boundary})
	if err != nil || len(events.GetEvents()) != 3 {
		t.Fatal("Граница событий нарушена", err)
	}
	start = time.Now()
	seen := make([]bool, 100001)
	count, pages := 0, 0
	for {
		pages++
		for _, item := range page.Contents {
			n := item.Sequence
			if n < 1 || n > 100000 || seen[n] {
				t.Fatalf("Дубль или сдвиг снимка: %d", n)
			}
			seen[n] = true
			count++
			if n == 39998 && (!item.Deleted || item.Text != "") {
				t.Fatal("Удаление потеряно")
			}
		}
		if page.NextBeforeSequence == 0 {
			break
		}
		page, err = f.contents.GetHistoryPage(f.owner, &pb.GetHistoryPageRequest{ChannelId: "general", BeforeSequence: page.NextBeforeSequence})
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 100000 || pages != 1000 {
		t.Fatalf("Потеря истории: %d записей, %d страниц", count, pages)
	}
	t.Logf("100000 записей через RPC, 1000 страниц: %s", time.Since(start))
}

// Не полагаемся на локаль PostgreSQL: домашняя база может использовать C.
var searchUpper, searchLower = searchCaseAlphabet()

func searchCaseAlphabet() (string, string) {
	var upper, lower strings.Builder
	for value := rune(0); value <= unicode.MaxRune; value++ {
		mapped := unicode.ToLower(value)
		if value != mapped {
			upper.WriteRune(value)
			lower.WriteRune(mapped)
		}
	}
	return upper.String(), lower.String()
}
