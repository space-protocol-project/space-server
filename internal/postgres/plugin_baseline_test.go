package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

// Счётчик вызовов pgx, включая запросы авторизации и транзакций.
type baselineSQL struct{ calls atomic.Int64 }

func (s *baselineSQL) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	s.calls.Add(1)
	return ctx
}
func (*baselineSQL) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type baselineSample struct {
	Operation    string    `json:"operation"`
	Objects      int       `json:"objects_per_type"`
	Round        int       `json:"round"`
	Milliseconds []float64 `json:"milliseconds"`
	SQLCalls     []int64   `json:"pgx_calls"`
	P50          float64   `json:"p50_ms"`
	P95          float64   `json:"p95_ms"`
	HeapBytes    uint64    `json:"combined_go_heap_bytes"`
}

func baselineCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func baselineSeed(t *testing.T, f *channelFixture, n int) {
	t.Helper()
	forumSection(t, f, "baseline_forum")
	feedSection(t, f, "baseline_feed")
	statements := []string{
		`INSERT INTO contents(channel_id,sequence,id,text,idempotency_key,author_id,request_text_hash)
SELECT 'general',n,'message-'||n,'baseline message '||n,'seed-'||n,$2,sha256(convert_to('baseline message '||n,'UTF8')) FROM generate_series(1,$1::int) n`,
		`INSERT INTO events(channel_id,sequence,content_sequence,cursor,type)
SELECT 'general',n,n,'event-'||n,'content.created' FROM generate_series(1,$1::int) n WHERE $2::text<>''`,
		`INSERT INTO forum_threads(id,channel_id,author_id,author_name,title,body,sequence,idempotency_key)
SELECT 'topic-'||n,'baseline_forum',$2,'Owner','baseline topic '||n,'baseline body',n,'seed-'||n FROM generate_series(1,$1::int) n`,
		`INSERT INTO forum_replies(id,channel_id,thread_id,author_id,author_name,body,sequence,idempotency_key)
SELECT 'reply-'||n,'baseline_forum','topic-1',$2,'Owner','baseline reply '||n,n,'reply-'||n FROM generate_series(1,$1::int) n`,
		`INSERT INTO feed_posts(id,channel_id,author_id,author_name,body,sequence,idempotency_key)
SELECT 'post-'||n,'baseline_feed',$2,'Owner','baseline post '||n,n,'seed-'||n FROM generate_series(1,$1::int) n`,
		`INSERT INTO feed_comments(id,channel_id,post_id,author_id,author_name,body,sequence,idempotency_key)
SELECT 'comment-'||n,'baseline_feed','post-1',$2,'Owner','baseline comment '||n,n,'comment-'||n FROM generate_series(1,$1::int) n`,
	}
	for _, sql := range statements {
		_, err := f.store.pool.Exec(f.ctx, sql, n, f.ownerID)
		baselineCheck(t, err)
	}
	_, err := f.store.pool.Exec(f.ctx, `UPDATE channels SET next_sequence=$1+1,next_event_sequence=$1+1;
`, n)
	baselineCheck(t, err)
	_, err = f.store.pool.Exec(f.ctx, `UPDATE forum_threads SET reply_count=$1 WHERE id='topic-1'`, n)
	baselineCheck(t, err)
	_, err = f.store.pool.Exec(f.ctx, `UPDATE feed_posts SET comment_count=$1 WHERE id='post-1'`, n)
	baselineCheck(t, err)
	_, err = f.store.pool.Exec(f.ctx, `UPDATE forum_threads SET hidden=true WHERE sequence%100=0;
UPDATE feed_posts SET hidden=true WHERE sequence%100=0;
UPDATE contents SET deleted=true,text='' WHERE sequence%100=0;
ANALYZE contents; ANALYZE forum_threads; ANALYZE forum_replies; ANALYZE feed_posts; ANALYZE feed_comments;`)
	baselineCheck(t, err)
}

func baselineOperations(t *testing.T, f *channelFixture) map[string]func() error {
	forum := pb.NewForumServiceClient(f.conn)
	feed := pb.NewFeedServiceClient(f.conn)
	search := pb.NewSpaceSearchServiceClient(f.conn)
	overview := pb.NewCommunityOverviewServiceClient(f.conn)
	return map[string]func() error{
		"history": func() error {
			r, e := f.contents.GetHistoryPage(f.owner, &pb.GetHistoryPageRequest{ChannelId: "general"})
			if e == nil && len(r.Contents) != 100 {
				return fmt.Errorf("history size %d", len(r.Contents))
			}
			return e
		},
		"topic": func() error {
			r, e := forum.GetForumThread(f.owner, &pb.GetForumThreadRequest{ChannelId: "baseline_forum", ThreadId: "topic-1"})
			if e == nil && r.Thread.Id != "topic-1" {
				return fmt.Errorf("wrong topic")
			}
			return e
		},
		"post": func() error {
			r, e := feed.GetFeedPost(f.owner, &pb.GetFeedPostRequest{ChannelId: "baseline_feed", PostId: "post-1"})
			if e == nil && r.Post.Id != "post-1" {
				return fmt.Errorf("wrong post")
			}
			return e
		},
		"search": func() error { _, e := search.SearchSpace(f.owner, &pb.SearchSpaceRequest{Query: "baseline"}); return e },
		"overview_forum": func() error {
			_, e := overview.GetCommunityOverview(f.owner, &pb.GetCommunityOverviewRequest{ChannelId: "baseline_forum"})
			return e
		},
		"overview_feed": func() error {
			_, e := overview.GetCommunityOverview(f.owner, &pb.GetCommunityOverviewRequest{ChannelId: "baseline_feed"})
			return e
		},
		"write_visible": func() error {
			key := fmt.Sprintf("write-%d", time.Now().UnixNano())
			r, e := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: key, IdempotencyKey: key})
			if e != nil {
				return e
			}
			page, e := f.contents.GetHistoryPage(f.owner, &pb.GetHistoryPageRequest{ChannelId: "general"})
			if e != nil {
				return e
			}
			for _, c := range page.Contents {
				if c.Id == r.Content.Id {
					return nil
				}
			}
			return fmt.Errorf("write not visible")
		},
	}
}

func baselineMeasure(t *testing.T, counter *baselineSQL, name string, n, round int, call func() error) baselineSample {
	t.Helper()
	for i := 0; i < 10; i++ {
		baselineCheck(t, call())
	}
	r := baselineSample{Operation: name, Objects: n, Round: round}
	for i := 0; i < 100; i++ {
		before := counter.calls.Load()
		start := time.Now()
		baselineCheck(t, call())
		r.Milliseconds = append(r.Milliseconds, float64(time.Since(start).Nanoseconds())/1e6)
		r.SQLCalls = append(r.SQLCalls, counter.calls.Load()-before)
	}
	sorted := append([]float64{}, r.Milliseconds...)
	sort.Float64s(sorted)
	r.P50 = sorted[49]
	r.P95 = sorted[94]
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	r.HeapBytes = memory.HeapAlloc
	return r
}

func baselineDump(t *testing.T, f *channelFixture, destination string) int64 {
	t.Helper()
	u, err := url.Parse(f.url)
	baselineCheck(t, err)
	q := u.Query()
	schema := q.Get("search_path")
	q.Del("search_path")
	u.RawQuery = q.Encode()
	cmd := exec.CommandContext(f.ctx, "pg_dump", "--dbname", u.String(), "--schema", schema, "--format=custom", "--file", destination)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pg_dump: %v %s", err, output)
	}
	info, err := os.Stat(destination)
	baselineCheck(t, err)
	return info.Size()
}

func TestPluginBaseline(t *testing.T) {
	output := os.Getenv("SPACE_BASELINE_OUTPUT")
	if output == "" {
		t.Skip("Отдельный измерительный прогон CI")
	}
	baselineCheck(t, os.MkdirAll(output, 0700))
	for _, n := range []int{1000, 10000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := newChannelFixtureTimeout(t, 15*time.Minute)
			config := f.store.pool.Config()
			counter := &baselineSQL{}
			config.ConnConfig.Tracer = counter
			f.store.pool.Close()
			pool, err := pgxpool.NewWithConfig(f.ctx, config)
			baselineCheck(t, err)
			f.store.pool = pool
			baselineSeed(t, f, n)
			restricted, _ := f.enroll(t, true)
			private := f.create(t, "baseline_private")
			f.acl(t, private, roleRule("member", permissions(false, false, false, false)))
			_, err = f.contents.GetHistoryPage(restricted, &pb.GetHistoryPageRequest{ChannelId: private.Id})
			requireCode(t, err, codes.NotFound)
			operations := baselineOperations(t, f)
			names := make([]string, 0, len(operations))
			for name := range operations {
				names = append(names, name)
			}
			sort.Strings(names)
			results := []baselineSample{}
			for round := 1; round <= 3; round++ {
				for _, name := range names {
					results = append(results, baselineMeasure(t, counter, name, n, round, operations[name]))
				}
			}
			dump := baselineDump(t, f, filepath.Join(output, fmt.Sprintf("fixture-%d.dump", n)))
			payload := map[string]any{"measurements": results, "backup_bytes": dump, "go": runtime.Version(), "cpu_count": runtime.NumCPU(), "memory_scope": "combined Go gRPC test client and server, not Flutter RSS", "sql_scope": "pgx QueryTracer calls including authorization; not PostgreSQL statement count", "seed": "n each: contents, topics, replies, posts, comments; writes appended by visibility samples"}
			data, err := json.MarshalIndent(payload, "", "  ")
			baselineCheck(t, err)
			baselineCheck(t, os.WriteFile(filepath.Join(output, fmt.Sprintf("baseline-%d.json", n)), data, 0600))
		})
	}
}
